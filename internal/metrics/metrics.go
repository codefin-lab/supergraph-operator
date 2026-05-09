package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	CompositionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "supergraph_compositions_total",
			Help: "Total number of supergraph compositions by status.",
		},
		[]string{"status"},
	)

	CompositionDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "supergraph_composition_duration_seconds",
			Help:    "Duration of supergraph composition in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	SubgraphsTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "supergraph_subgraphs_total",
			Help: "Current number of SubgraphSchema resources.",
		},
	)

	CompositionsSkipped = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "supergraph_compositions_skipped_total",
			Help: "Total number of compositions skipped due to unchanged schemas.",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		CompositionsTotal,
		CompositionDuration,
		SubgraphsTotal,
		CompositionsSkipped,
	)
}
