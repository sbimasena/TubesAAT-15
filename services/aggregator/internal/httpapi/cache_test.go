package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

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
	s.EnableReadCache()
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
