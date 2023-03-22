package main

import (
	"log"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/wait"
	basecmd "sigs.k8s.io/custom-metrics-apiserver/pkg/cmd"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"
)

type metricsAdaptor struct {
	basecmd.AdapterBase
}

func (a *metricsAdaptor) makeProviderOrDie(serviceUrls, serviceNames string) provider.CustomMetricsProvider {
	client, err := a.DynamicClient()
	if err != nil {
		log.Fatalf("unable to construct dynamic client: %v", err)
	}

	mapper, err := a.RESTMapper()
	if err != nil {
		log.Fatalf("unable to construct discovery REST mapper: %v", err)
	}

	var urls []string
	if serviceUrls != "" {
		urls = append(urls, strings.Split(serviceUrls,";")...)
	}

	var names []string
	if serviceNames != "" {
		names = append(names, strings.Split(serviceNames,";")...)
	}

	return NewMetricProvider(&ServiceList{serviceUrls: urls, serviceNames: names}, client, mapper)
}

func main() {
	cmd := &metricsAdaptor{}
	cmd.Flags().Parse(os.Args)

	serviceUrls := os.Getenv("TARGET_SERVICE_URLS")
	serviceNames := os.Getenv("TARGET_SERVICE_NAME")
	if serviceNames == "" && serviceUrls == "" {
		log.Fatalln("both `TARGET_SERVICE_URLS` and `TARGET_SERVICE_NAME` are not set.")
	}

	provider := cmd.makeProviderOrDie(serviceUrls, serviceNames)
	cmd.WithCustomMetrics(provider)

	log.Println("start custom metrics provider")
	if err := cmd.Run(wait.NeverStop); err != nil {
		log.Fatalf("unable to run custom metrics adapter: %v", err)
	}	
}
