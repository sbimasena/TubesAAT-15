package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type cacheRepository struct {
	calls int
	fail  bool
}

func (r *cacheRepository) List(context.Context, store.Filter, string) ([]domain.HazardEvent, error) {
	r.calls++
	if r.fail {
		return nil, errors.New("offline")
	}
	return []domain.HazardEvent{{HazardID: "one"}}, nil
}
func (r *cacheRepository) Upsert(context.Context, int64, []domain.HazardEvent, string) ([]domain.HazardEvent, error) {
	return nil, nil
}
func (r *cacheRepository) Ping(context.Context) error { return nil }
func TestReadCacheExpiresAndDoesNotCacheErrors(t *testing.T) {
	r := &cacheRepository{}
	s := NewServer(r, nil, nil)
	s.EnableReadCache(time.Second)
	f := store.Filter{Source: "BMKG", Limit: 100}
	for i := 0; i < 2; i++ {
		if _, err := s.readHazards(context.Background(), f, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if r.calls != 1 {
		t.Fatal("cache miss on repeated filter")
	}
	time.Sleep(1010 * time.Millisecond)
	r.fail = true
	for i := 0; i < 2; i++ {
		if _, err := s.readHazards(context.Background(), f, "test"); err == nil {
			t.Fatal("stale cache served")
		}
	}
	if r.calls != 3 {
		t.Fatal("failure cached")
	}
	r.fail = false
	for i := 0; i < 140; i++ {
		f.Limit = i + 1
		if _, err := s.readHazards(context.Background(), f, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.cache.entries) > 128 {
		t.Fatal("unbounded cache")
	}
}

func TestReadCacheCanBeDisabled(t *testing.T) {
	r := &cacheRepository{}
	s := NewServer(r, nil, nil)
	s.EnableReadCache(0)
	f := store.Filter{Source: "BMKG", Limit: 100}
	for i := 0; i < 2; i++ {
		if _, err := s.readHazards(context.Background(), f, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if r.calls != 2 {
		t.Fatalf("repository calls = %d, want 2 with cache disabled", r.calls)
	}
	status, ok := s.CacheStatus().(map[string]any)
	if !ok || status["enabled"] != false {
		t.Fatalf("cache status = %#v, want disabled", status)
	}
}

func TestReadCacheReportsHitRatio(t *testing.T) {
	r := &cacheRepository{}
	s := NewServer(r, nil, nil)
	s.EnableReadCache(time.Second)
	f := store.Filter{Source: "BMKG", Limit: 100}
	for i := 0; i < 2; i++ {
		if _, err := s.readHazards(context.Background(), f, "test"); err != nil {
			t.Fatal(err)
		}
	}
	status, ok := s.CacheStatus().(map[string]any)
	if !ok || status["enabled"] != true || status["hits"] != uint64(1) || status["misses"] != uint64(1) || status["hit_ratio"] != 0.5 {
		t.Fatalf("cache status = %#v", status)
	}
}

func TestReadCacheExportsPrometheusMetrics(t *testing.T) {
	r := &cacheRepository{}
	s := NewServer(r, nil, nil)
	s.EnableReadCache(time.Second)
	if testutil.ToFloat64(cacheEnabledMetric) != 1 {
		t.Fatal("enabled gauge is not 1")
	}
	f := store.Filter{Source: "PVMBG", HazardType: "VOLCANIC", Limit: 7}
	hits, misses := testutil.ToFloat64(cacheHitsMetric), testutil.ToFloat64(cacheMissesMetric)
	for i := 0; i < 2; i++ {
		if _, err := s.readHazards(context.Background(), f, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(cacheHitsMetric); got != hits+1 {
		t.Fatalf("hits = %v, want %v", got, hits+1)
	}
	if got := testutil.ToFloat64(cacheMissesMetric); got != misses+1 {
		t.Fatalf("misses = %v, want %v", got, misses+1)
	}
	if ratio := testutil.ToFloat64(cacheRatioMetric); ratio <= 0 || ratio > 1 {
		t.Fatalf("hit ratio = %v", ratio)
	}
}
