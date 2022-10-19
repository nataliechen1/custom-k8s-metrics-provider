package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"sync"
	"time"

	apierr "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/metrics/pkg/apis/custom_metrics"

	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider/helpers"
)

// The names of the metrics we are creating.
const heapInUse = "heap_in_use"
const numberOfShards = "number_of_shards"
const contentBytes = "content_bytes"
const indexBytes = "index_bytes"

// Map of supported metrics to their corresponding output column name.
var supportedMetrics = map[string]string{
	heapInUse:               "heap_in_use",
	numberOfShards:         "number_of_shards",
	contentBytes:  "content_bytes",
	indexBytes: "index_bytes",
}

type ZoektMetricsProvider struct {
	TargetUrls 			[]string
	client               dynamic.Interface
	mapper               apimeta.RESTMapper
	dataMux              sync.Mutex
	podInfo              map[string]map[string]float64
	supportedMetricInfos []provider.CustomMetricInfo
}

func NewMetricProvider(targets []string, k8sClient dynamic.Interface, mapper apimeta.RESTMapper) *ZoektMetricsProvider {
	var supportedMetricInfos []provider.CustomMetricInfo
	for metricName, _ := range supportedMetrics {
		metricInfo := provider.CustomMetricInfo{
			GroupResource: schema.GroupResource{Group: "", Resource: "pods"},
			Metric:        metricName,
			Namespaced:    true,
		}
		supportedMetricInfos = append(supportedMetricInfos, metricInfo)
	}
	
	provider := &ZoektMetricsProvider {
		TargetUrls: targets,
		client:               k8sClient,
		mapper:               mapper,
		podInfo:              make(map[string]map[string]float64),
		supportedMetricInfos: supportedMetricInfos,
	}
	go provider.runMetricsLoop()
	return provider
}

// GetMetricByName returns the the pod metric.
func (p *ZoektMetricsProvider) GetMetricByName(ctx context.Context, name types.NamespacedName, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValue, error) {
	if _, ok := supportedMetrics[info.Metric]; !ok {
		return nil, provider.NewMetricNotFoundError(info.GroupResource, info.Metric)
	}
	if info.GroupResource.Resource != "pods" {
		return nil, provider.NewMetricNotFoundError(info.GroupResource, info.Metric)
	}

	p.dataMux.Lock()
	defer p.dataMux.Unlock()

	podInfo, ok := p.podInfo[name.Name]
	if !ok {
		log.Printf("%v", p.podInfo)
		return nil, provider.NewMetricNotFoundForError(info.GroupResource, info.Metric, name.String())
	}
	metric, ok := podInfo[info.Metric]
	if !ok {
		return nil, provider.NewMetricNotFoundForError(info.GroupResource, info.Metric, name.String())
	}
	return p.metricFor(metric, name, info)
}

// GetMetricBySelector returns the the pod metric for a pod selector.
func (p *ZoektMetricsProvider) GetMetricBySelector(ctx context.Context, namespace string, selector labels.Selector, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValueList, error) {
	names, err := helpers.ListObjectNames(p.mapper, p.client, namespace, selector, info)
	if err != nil {
		return nil, err
	}

	res := make([]custom_metrics.MetricValue, 0, len(names))
	for _, name := range names {
		namespacedName := types.NamespacedName{Name: name, Namespace: namespace}
		value, err := p.GetMetricByName(ctx, namespacedName, info, metricSelector)
		if err != nil || value == nil {
			if apierr.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		res = append(res, *value)
	}

	return &custom_metrics.MetricValueList{
		Items: res,
	}, nil
}

// ListAllMetrics returns the single metric defined by this provider. Could be extended to return additional Pixie metrics.
func (p *ZoektMetricsProvider) ListAllMetrics() []provider.CustomMetricInfo {
	return p.supportedMetricInfos
}

func (p *ZoektMetricsProvider) runMetricsLoop() {
	ctx := context.Background()
	for {
		p.computeMetrics(ctx)
		<-time.After(15 * time.Second)
	}
}

func (p *ZoektMetricsProvider) metricFor(value float64, name types.NamespacedName, info provider.CustomMetricInfo) (*custom_metrics.MetricValue, error) {
	// construct a reference referring to the described object
	objRef, err := helpers.ReferenceFor(p.mapper, name, info)
	if err != nil {
		return nil, err
	}

	return &custom_metrics.MetricValue{
		DescribedObject: objRef,
		Metric: custom_metrics.MetricIdentifier{
			Name: info.Metric,
		},
		Timestamp: metav1.Time{time.Now()},
		Value:     *resource.NewMilliQuantity(int64(value*1000), resource.DecimalSI),
	}, nil
}

func (p *ZoektMetricsProvider) computeMetrics(ctx context.Context) {
	for _, url := range p.TargetUrls {
		stats, err := p.getMetrics(ctx, url)
		if err != nil {
			log.Printf("get metrics from %s err:%v", url, err)
			continue
		}
		log.Printf("%s=%+v", url, stats)
		p.podInfo[stats.HostName] = make(map[string]float64, 4)
		p.podInfo[stats.HostName]["heap_in_use"] = float64(stats.HeapInuse)
		p.podInfo[stats.HostName]["number_of_shards"] = float64(stats.RepoStats.Shards)
		p.podInfo[stats.HostName]["content_bytes"] = float64(stats.RepoStats.ContentBytes)
		p.podInfo[stats.HostName]["index_bytes"] = float64(stats.RepoStats.IndexBytes)
	}
}

type Stats struct {
	Time int64 `json:"time"`
	// runtime
	GoVersion    string `json:"go_version"`
	GoOs         string `json:"go_os"`
	GoArch       string `json:"go_arch"`
	CpuNum       int    `json:"cpu_num"`
	GoroutineNum int    `json:"goroutine_num"`
	Gomaxprocs   int    `json:"gomaxprocs"`
	CgoCallNum   int64  `json:"cgo_call_num"`
	// memory
	MemoryAlloc      uint64 `json:"memory_alloc"`
	MemoryTotalAlloc uint64 `json:"memory_total_alloc"`
	MemorySys        uint64 `json:"memory_sys"`
	MemoryLookups    uint64 `json:"memory_lookups"`
	MemoryMallocs    uint64 `json:"memory_mallocs"`
	MemoryFrees      uint64 `json:"memory_frees"`
	// stack
	StackInUse uint64 `json:"memory_stack"`
	// heap
	HeapAlloc    uint64 `json:"heap_alloc"`
	HeapSys      uint64 `json:"heap_sys"`
	HeapIdle     uint64 `json:"heap_idle"`
	HeapInuse    uint64 `json:"heap_inuse"`
	HeapReleased uint64 `json:"heap_released"`
	HeapObjects  uint64 `json:"heap_objects"`
	// garbage collection
	GcNext           uint64    `json:"gc_next"`
	GcLast           uint64    `json:"gc_last"`
	GcNum            uint32    `json:"gc_num"`
	GcPerSecond      float64   `json:"gc_per_second"`
	GcPausePerSecond float64   `json:"gc_pause_per_second"`
	GcPause          []float64 `json:"gc_pause"`
	// repo stats
	RepoStats struct {
		Repos 						int 	`json:"Repos"`
		Shards 						int		`json:"Shards"`
		Documents 					int		`json:"Documents"`
		IndexBytes 					int64	`json:"IndexBytes"`
		ContentBytes 				int64	`json:"ContentBytes"`
		NewLinesCount 				uint64	`json:"NewLinesCount"`
		DefaultBranchNewLinesCount	uint64	`json:"DefaultBranchNewLinesCount"`
		OtherBranchesNewLinesCount	uint64	`json:"OtherBranchesNewLinesCount"`
	}`json:"repo_stats"`
	HostName	string `json:"host_name"`
}

func (p *ZoektMetricsProvider) getMetrics(ctx context.Context, url string) (*Stats, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
	}

	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(30*time.Second))
	defer cancel()

	req, err := http.NewRequest("GET", url + "/stats", nil)
	if err != nil {
		return nil, err
	}

	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get stats: status %v", resp.StatusCode)
	}
	defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body) // response body is []byte
	var result Stats
	if err := json.Unmarshal(body, &result); err != nil {   // Parse []byte to go struct pointer
    	log.Println("Can not unmarshal JSON")
		return nil, err
	}

	return &result, nil
}
