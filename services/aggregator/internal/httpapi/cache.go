package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
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
	ttl     time.Duration
	hits    atomic.Uint64
	misses  atomic.Uint64
}

// EnableReadCache bounds per-instance memory and freshness. A non-positive TTL
// disables the cache so P2 can compare cache and storage scaling directly.
func (s *Server) EnableReadCache(ttl time.Duration) {
	if ttl <= 0 {
		s.cache = nil
		return
	}
	s.cache = &readCache{entries: make(map[store.Filter]cachedRead), ttl: ttl}
}

// CacheStatus reports bounded cache state and the measured hit ratio.
func (s *Server) CacheStatus() any {
	if s.cache == nil {
		return map[string]any{"enabled": false}
	}
	c := s.cache
	c.mu.Lock()
	entries, bytes := len(c.entries), c.bytes
	c.mu.Unlock()
	hits, misses := c.hits.Load(), c.misses.Load()
	total := hits + misses
	ratio := 0.0
	if total > 0 {
		ratio = float64(hits) / float64(total)
	}
	return map[string]any{
		"enabled": true, "ttl_ms": c.ttl.Milliseconds(), "hits": hits,
		"misses": misses, "hit_ratio": ratio, "entries": entries, "bytes": bytes,
	}
}

func (s *Server) readHazards(ctx context.Context, f store.Filter, id string) ([]domain.HazardEvent, error) {
	if s.cache == nil {
		return s.repository.List(ctx, f, id)
	}
	c := s.cache
	c.mu.Lock()
	entry, ok := c.entries[f]
	c.mu.Unlock()
	if ok && time.Since(entry.at) < c.ttl {
		c.hits.Add(1)
		return entry.events, nil
	}
	c.misses.Add(1)
	key := fmt.Sprintf("%s|%s|%s|%d", f.Source, f.HazardType, f.Since.Format(time.RFC3339Nano), f.Limit)
	result := c.pending.DoChan(key, func() (any, error) {
		// A previous in-flight request may have filled the cache before DoChan.
		c.mu.Lock()
		entry, ok := c.entries[f]
		c.mu.Unlock()
		if ok && time.Since(entry.at) < c.ttl {
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
