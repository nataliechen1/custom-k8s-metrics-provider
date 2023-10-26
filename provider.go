package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
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
const jvmHeapUsed = "jvm.memory.heap.used"

// Map of supported metrics to their corresponding output column name.
var supportedMetrics = map[string]string{
	heapInUse:              "heap_in_use",
	numberOfShards:         "number_of_shards",
	contentBytes:  			"content_bytes",
	indexBytes: 			"index_bytes",
	jvmHeapUsed: 			"jvm.memory.heap.used",
}

type ServiceList struct {
	serviceUrls 		[]string
	serviceNames		[]string
}

func (s *ServiceList) getTargetUrls() (map[string][]string, error) {
	targets := make(map[string][]string)
	if s.serviceUrls != nil {
		targets["default"] = s.serviceUrls
	} else {
		for _, name := range s.serviceNames {
			ss := strings.Split(name, ":")
			addresses, err := net.LookupHost(ss[0])
			if err != nil {
				log.Printf("can't resolve %s", ss[0])
				return nil, err
			}
			port := "80"
			if len(ss) > 1 {
				port = ss[1]
			}
			namespace := strings.Split(ss[0], ".")[1]
			for _, addr := range addresses {

				targets[namespace] = append(targets[namespace], fmt.Sprintf("http://%s:%s", addr, port))
			}
		}
	}
	return targets, nil
}

type ZoektMetricsProvider struct {
	serviceList			 *ServiceList
	client               dynamic.Interface
	mapper               apimeta.RESTMapper
	dataMux              sync.Mutex
	podInfo              map[string]map[string]float64
	supportedMetricInfos []provider.CustomMetricInfo
}

func NewMetricProvider(list *ServiceList, k8sClient dynamic.Interface, mapper apimeta.RESTMapper) *ZoektMetricsProvider {
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
		serviceList: 		  list,
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

	key := fmt.Sprintf("%s.%s", name.Namespace, name.Name)
	podInfo, ok := p.podInfo[key]
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
		podInfo := p.computeMetrics(ctx)
		if podInfo != nil {
			p.dataMux.Lock()
			p.podInfo = podInfo
			p.dataMux.Unlock()
		}
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

func (p *ZoektMetricsProvider) computeMetrics(ctx context.Context) map[string]map[string]float64 {
	targets, err := p.serviceList.getTargetUrls()
	if err != nil {
		log.Printf("get target urls err: %v", err)
		return nil
	}
	podInfo := make(map[string]map[string]float64)
	for namespace, urls := range targets {
		for _, url := range urls {
			ret, err := p.getMetrics(ctx, url)
			if err != nil {
				log.Printf("get metrics from %s err:%v", url, err)
				continue
			}
			if stats, ok := ret.(map[string]interface{}); ok {
				log.Printf("url=%s metrics=%+v", url, stats)
				hostName := fmt.Sprintf("%s", stats["host_name"])
				podName := fmt.Sprintf("%s.%s", namespace, hostName)
				if strings.HasPrefix(hostName, "zoekt") {
					if repoStats, ok := stats["repo_stats"]; ok {
						podInfo[podName] = make(map[string]float64, 4)
						if _, ok := stats["heap_inuse"]; ok {
							podInfo[podName][heapInUse] = float64(stats["heap_inuse"].(float64))
						}
						if repoStatsMap, ok := repoStats.(map[string]interface{}); ok {
							podInfo[podName]["number_of_shards"] = float64(repoStatsMap["Shards"].(float64))
							podInfo[podName]["content_bytes"] = float64(repoStatsMap["ContentBytes"].(float64))
							podInfo[podName]["index_bytes"] = float64(repoStatsMap["IndexBytes"].(float64))
						}
					}
				} else if strings.HasPrefix(hostName, "conav") {
					podInfo[podName] = make(map[string]float64, 3)
					podInfo[podName]["jvm.memory.heap.used"], _ = strconv.ParseFloat(stats["jvm.memory.heap.used"].(string), 64)
					podInfo[podName]["jvm.gc.G1-Old-Generation.time"], _ = strconv.ParseFloat(stats["jvm.gc.G1-Old-Generation.time"].(string), 64)
					podInfo[podName]["jvm.gc.G1-Young-Generation.time"], _ = strconv.ParseFloat(stats["jvm.gc.G1-Young-Generation.time"].(string), 64)
				}
				log.Printf("podName=%s stats=%+v", podName, podInfo[podName])
			}
		}
	}
	return podInfo
}

func (p *ZoektMetricsProvider) getMetrics(ctx context.Context, url string) (interface{}, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Transport: tr,
	}

	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(5*time.Second))
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
	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {   // Parse []byte to go struct pointer
    	log.Println("Can not unmarshal JSON")
		return nil, err
	}

	return result, nil
}
