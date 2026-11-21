package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// eventAge is the SLI from Aggregator ingestion to the durable notification
	// acknowledgement. The journal keeps the raw value for offline checks.
	eventAge = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "notification_event_age_seconds",
		Help:    "Aggregator ingestion to durable notification acknowledgement.",
		Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 30, 60},
	})
	handledEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "notification_events_total",
		Help: "Notification events acknowledged, by durable result.",
	}, []string{"result"})
)

func metricsHandler() http.Handler { return promhttp.Handler() }
