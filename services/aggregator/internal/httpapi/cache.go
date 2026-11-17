package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
	"golang.org/x/sync/singleflight"
)

type cachedRead struct {
	events []domain.HazardEvent
	at     time.Time
	size   int
}
type readCache struct {
	mu      sync.Mutex
	entries map[store.Filter]cachedRead
	pending singleflight.Group
	bytes   int
}

// EnableReadCache bounds per-instance memory and freshness. Audit reads bypass it.
func (s *Server) EnableReadCache() { s.cache = &readCache{entries: make(map[store.Filter]cachedRead)} }

func (s *Server) readHazards(ctx context.Context, f store.Filter, id string) ([]domain.HazardEvent, error) {
	if s.cache == nil {
		return s.repository.List(ctx, f, id)
	}
	c := s.cache
	c.mu.Lock()
	entry, ok := c.entries[f]
	c.mu.Unlock()
	if ok && time.Since(entry.at) < time.Second {
		return entry.events, nil
	}
	key := fmt.Sprintf("%s|%s|%s|%d", f.Source, f.HazardType, f.Since.Format(time.RFC3339Nano), f.Limit)
	result := c.pending.DoChan(key, func() (any, error) {
		// A previous in-flight request may have filled the cache before DoChan.
		c.mu.Lock()
		entry, ok := c.entries[f]
		c.mu.Unlock()
		if ok && time.Since(entry.at) < time.Second {
			return entry.events, nil
		}
		started := time.Now()
		events, err := s.repository.List(ctx, f, id)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(events)
		if err != nil {
			return nil, err
		}
		if len(raw) > 1<<20 {
			return events, nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.entries) >= 128 || c.bytes+len(raw) > 16<<20 {
			clear(c.entries)
			c.bytes = 0
		}
		c.bytes -= c.entries[f].size
		c.entries[f] = cachedRead{events: events, at: started, size: len(raw)}
		c.bytes += len(raw)
		return events, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return nil, value.Err
		}
		return value.Val.([]domain.HazardEvent), nil
	}
}
