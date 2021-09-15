package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// filtersGauge includes filters that already exist at startup.
	filtersGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "transflect_envoyfilters",
		Help: "Current number of EnvoyFilters managed by transflect",
	})
	processedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "transflect_operations_total",
		Help: "The total number of transflect operations processed",
	},
		[]string{
			"status", // success, error
			"type",   // upsert, delete
		},
	)
	preprocessErrCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "transflect_preprocess_error_total",
		Help: "The total number of replicaset ids popped off the queue that errored before being processed",
	})
	ignoredCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "transflect_ignored_total",
		Help: "The total number of replicaset ids popped off the queue that were ignored and didn't need processing",
	})
)
