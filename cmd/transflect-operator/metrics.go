package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"k8s.io/client-go/util/workqueue"
)

var (
	// filtersGauge includes filters that already exist at startup.
	filtersGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "transflect_envoyfilters",
		Help: "Current number of EnvoyFilters managed by transflect.",
	})
	leaderGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "transflect_leader",
		Help: "Currently holding leadership.",
	})
	processedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "transflect_operations_total",
		Help: "The total number of transflect operations processed.",
	},
		[]string{
			"status", // success, error
			"type",   // upsert, delete
		},
	)
	preprocessErrCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "transflect_preprocess_error_total",
		Help: "The total number of replicaset ids popped off the queue that errored before being processed.",
	})
	ignoredCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "transflect_ignored_total",
		Help: "The total number of replicaset ids popped off the queue that were ignored and didn't need processing.",
	})

	// queue metrics.
	qDepthGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "workqueue_depth",
		Help: "Current depth of the work queue.",
	}, []string{"queue_name"})

	qAddsCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "workqueue_adds_total",
		Help: "Total number of items added to the work queue.",
	}, []string{"queue_name"})

	qLatencyHistogram = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "workqueue_latency_seconds",
		Help: "How long an item stays in the work queue.",
	}, []string{"queue_name"})

	qWorkDurationHistogram = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "workqueue_work_duration_seconds",
		Help: "How long processing an item from the work queue takes.",
	}, []string{"queue_name"})

	qUnfinshedWorkGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "workqueue_unfinished_work_seconds",
		Help: "How long an item has remained unfinished in the work queue.",
	}, []string{"queue_name"})

	qLongestRunningProcessorGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "workqueue_longest_running_processor_seconds",
		Help: "Duration of the longest running processor in the work queue.",
	}, []string{"queue_name"})

	qRetriesCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "workqueue_retries_total",
		Help: "Total number retires as timeout re-adds.",
	}, []string{"queue_name"})
)

// metricsProvider implements workqueue.MetricsProvider, also see
// https://pkg.go.dev/k8s.io/client-go@v0.21.0/util/workqueue#MetricsProvider
// https://gitlab.com/gitlab-org/build/omnibus-mirror/prometheus/blob/8ac15703b040b18c81307d2a905393f6f29be096/discovery/kubernetes/client_metrics.go
type metricsProvider struct{}

func (metricsProvider) NewDepthMetric(name string) workqueue.GaugeMetric {
	return qDepthGauge.WithLabelValues(name)
}

func (metricsProvider) NewAddsMetric(name string) workqueue.CounterMetric {
	return qAddsCounter.WithLabelValues(name)
}

func (metricsProvider) NewLatencyMetric(name string) workqueue.HistogramMetric {
	return qLatencyHistogram.WithLabelValues(name)
}

func (metricsProvider) NewWorkDurationMetric(name string) workqueue.HistogramMetric {
	return qWorkDurationHistogram.WithLabelValues(name)
}

func (metricsProvider) NewUnfinishedWorkSecondsMetric(name string) workqueue.SettableGaugeMetric {
	return qUnfinshedWorkGauge.WithLabelValues(name)
}

func (metricsProvider) NewLongestRunningProcessorSecondsMetric(name string) workqueue.SettableGaugeMetric {
	return qLongestRunningProcessorGauge.WithLabelValues(name)
}

func (metricsProvider) NewRetriesMetric(name string) workqueue.CounterMetric {
	return qRetriesCounter.WithLabelValues(name)
}
