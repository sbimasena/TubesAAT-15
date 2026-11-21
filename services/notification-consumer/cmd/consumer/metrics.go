package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// eventAge covers notifications across all sources. The journal keeps a separate pre-append age.
	eventAge = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "notification_event_age_seconds",
		Help:    "Aggregator ingestion to durable notification acknowledgement.",
		Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 30, 60},
	})
	coastalCriticalAcknowledged = promauto.NewCounter(prometheus.CounterOpts{
		Name: "notification_pantai_critical_acknowledged_total",
		Help: "Journaled critical Pantai notifications with a successful local ACK, including recovery from ACK failures tracked in this process.",
	})
	coastalCriticalAge = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "notification_pantai_critical_age_seconds",
		Help:    "Aggregator ingestion to successful local ACK of counted critical Pantai notifications with valid timestamps.",
		Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 30, 60},
	})
	handledEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "notification_events_total",
		Help: "Notification events acknowledged, by durable result.",
	}, []string{"result"})
)

func metricsHandler() http.Handler { return promhttp.Handler() }
