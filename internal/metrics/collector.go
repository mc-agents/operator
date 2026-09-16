// Package metrics exports what the operator writes to status, so a dashboard can see bots stuck at
// Waiting or a server that is not Ready without scraping every CR. Nothing bot-identifying is in
// the labels: the metrics port is served without auth.
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/mc-agents/operator/api/v1alpha1"
)

const scrapeTimeout = 5 * time.Second

var (
	botsDesc = prometheus.NewDesc(
		"mc_agents_bots",
		"MinecraftBots by namespace, kind, phase and link.",
		[]string{"namespace", "kind", "phase", "link"}, nil,
	)
	serversReadyDesc = prometheus.NewDesc(
		"mc_agents_mcpserver_ready",
		"MCPServers in the namespace whose Ready condition is True.",
		[]string{"namespace"}, nil,
	)
)

// Register puts a collector on the manager's registry that lists from the cache on every scrape,
// so the counts are whatever the operator itself sees, with no bookkeeping in the reconcilers.
func Register(reader client.Reader) error {
	return ctrlmetrics.Registry.Register(collector{reader: reader})
}

type collector struct {
	reader client.Reader
}

func (collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- botsDesc
	ch <- serversReadyDesc
}

// Collect reports nothing rather than an error when a list fails: before the cache has synced
// there is no number to give, and the registry would otherwise fail the whole scrape. The cache
// blocks a list until its informer has synced, so the scrape is bounded rather than left hanging.
func (c collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	var bots v1alpha1.MinecraftBotList
	if err := c.reader.List(ctx, &bots); err == nil {
		type key struct{ namespace, kind, phase, link string }
		counts := map[key]int{}
		for _, bot := range bots.Items {
			counts[key{bot.Namespace, string(bot.Spec.Kind), string(bot.Status.Phase), string(bot.Status.Link)}]++
		}
		for k, n := range counts {
			ch <- prometheus.MustNewConstMetric(botsDesc, prometheus.GaugeValue, float64(n), k.namespace, k.kind, k.phase, k.link)
		}
	}

	var servers v1alpha1.MCPServerList
	if err := c.reader.List(ctx, &servers); err == nil {
		ready := map[string]int{}
		for _, server := range servers.Items {
			n := ready[server.Namespace]
			if meta.IsStatusConditionTrue(server.Status.Conditions, v1alpha1.ConditionReady) {
				n++
			}
			ready[server.Namespace] = n
		}
		for namespace, n := range ready {
			ch <- prometheus.MustNewConstMetric(serversReadyDesc, prometheus.GaugeValue, float64(n), namespace)
		}
	}
}
