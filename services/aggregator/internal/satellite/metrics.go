package satellite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	processorReady   = promauto.NewGauge(prometheus.GaugeOpts{Name: "bnpb_satellite_processor_ready", Help: "1 only after durable state and pending effects are recovered."})
	processedOffset  = promauto.NewGauge(prometheus.GaugeOpts{Name: "bnpb_satellite_processed_offset", Help: "Last fully materialized satellite offset."})
	offsetLag        = promauto.NewGauge(prometheus.GaugeOpts{Name: "bnpb_satellite_offset_lag", Help: "Latest observed session offset minus fully materialized offset."})
	eventLag         = promauto.NewGauge(prometheus.GaugeOpts{Name: "bnpb_satellite_event_time_lag_seconds", Help: "Session virtual time minus maximum processed event time."})
	lateDetections   = promauto.NewGauge(prometheus.GaugeOpts{Name: "bnpb_satellite_late_detections", Help: "Retained late detection identities in the durable checkpoint."})
	singleDetections = promauto.NewGauge(prometheus.GaugeOpts{Name: "bnpb_satellite_single_detections", Help: "Isolated eligible detections, not fires."})
	commitLatency    = promauto.NewHistogram(prometheus.HistogramOpts{Name: "bnpb_satellite_ingest_duration_seconds", Help: "Time from batch handler entry through replicated canonical commit and checkpoint completion.", Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60}})
)

// ObserveSession must be called by the owning worker, like Process. Lags use the
// assistant's virtual clock, so a 60x feed is not compared to host wall time.
func (p *Processor) ObserveSession(latest int64, virtualNow time.Time) {
	offset, err := p.Offset()
	if err != nil {
		return
	}
	offsetLag.Set(float64(max(0, latest-offset)))
	if !p.checkpoint.State.MaxEventTime.IsZero() {
		eventLag.Set(max(0, virtualNow.Sub(p.checkpoint.State.MaxEventTime).Seconds()))
	}
}
func (p *Processor) reportMetrics() {
	offset, err := p.Offset()
	if err != nil {
		return
	}
	processorReady.Set(1)
	processedOffset.Set(float64(offset))
	lateDetections.Set(float64(p.checkpoint.State.LateDetections))
	singleDetections.Set(float64(p.checkpoint.State.Singles))
}

// StoredStatus exposes persisted progress even from a follower without opening
// another SSE connection. It never includes raw detections or credentials.
func StoredStatus(ctx context.Context, repo interface {
	LoadCursor(context.Context, string) (string, error)
}) any {
	raw, err := repo.LoadCursor(ctx, "SATELIT")
	if err != nil {
		return map[string]any{"state": "checkpoint_unavailable"}
	}
	if raw == "" {
		return map[string]any{"state": "no_checkpoint"}
	}
	var checkpoint recoveryCheckpoint
	if len(raw) > 16<<20 || json.Unmarshal([]byte(raw), &checkpoint) != nil || checkpoint.Version != 1 || checkpoint.State == nil {
		return map[string]any{"state": "invalid_checkpoint"}
	}
	pending := len(checkpoint.Pending.Upserts) + len(checkpoint.Pending.Retracted)
	var processed *int64
	if pending == 0 {
		value := checkpoint.State.Offset
		processed = &value
	}
	return map[string]any{"state": "checkpoint_present", "processed_offset": processed, "durable_offset": checkpoint.State.Offset, "pending_effects": pending, "late_detections": checkpoint.State.LateDetections, "single_detections": checkpoint.State.Singles, "watermark": checkpoint.State.Watermark()}
}
