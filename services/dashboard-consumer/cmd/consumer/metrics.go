package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	eventAge = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "dashboard_event_age_seconds",
		Help:    "Aggregator ingestion to durable dashboard acknowledgement.",
		Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 30, 60},
	})
	handledEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dashboard_events_total",
		Help: "Dashboard events acknowledged or rejected, by result.",
	}, []string{"result"})
)

func metricsHandler() http.Handler { return promhttp.Handler() }
