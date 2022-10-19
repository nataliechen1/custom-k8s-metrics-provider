package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/wait"
	basecmd "sigs.k8s.io/custom-metrics-apiserver/pkg/cmd"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"
)

type metricsAdaptor struct {
	basecmd.AdapterBase
}

func (a *metricsAdaptor) makeProviderOrDie(serviceUrls, serviceName, servicePort string) provider.CustomMetricsProvider {
	var targets []string
	if serviceUrls != "" {
		targets = append(targets, strings.Split(serviceUrls,";")...)
	} else {
		addresses, err := net.LookupHost(serviceName)
		if err != nil {
			log.Fatalf("can't resolve %s", serviceName)
		}
		for _, addr := range addresses {
			targets = append(targets, fmt.Sprintf("http://%s:%s", addr, servicePort))
		}
	}

	client, err := a.DynamicClient()
	if err != nil {
		log.Fatalf("unable to construct dynamic client: %v", err)
	}

	mapper, err := a.RESTMapper()
	if err != nil {
		log.Fatalf("unable to construct discovery REST mapper: %v", err)
	}

	return NewMetricProvider(targets, client, mapper)
}

func main() {
	cmd := &metricsAdaptor{}
	cmd.Flags().Parse(os.Args)

	serviceUrls := os.Getenv("TARGET_SERVICE_URLS")
	serviceName := os.Getenv("TARGET_SERVICE_NAME")
	if serviceName == "" && serviceUrls == "" {
		log.Fatalln("both `TARGET_SERVICE_URLS` and `TARGET_SERVICE_NAME` are not set.")
	}

	servicePort := os.Getenv("TARGET_SERVICE_PORT")
	if servicePort == "" {
		servicePort = "80"
	}

	provider := cmd.makeProviderOrDie(serviceUrls, serviceName, servicePort)
	cmd.WithCustomMetrics(provider)

	log.Println("start custom metrics provider")
	if err := cmd.Run(wait.NeverStop); err != nil {
		log.Fatalf("unable to run custom metrics adapter: %v", err)
	}	
}
