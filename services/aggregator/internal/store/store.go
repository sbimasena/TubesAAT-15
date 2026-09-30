package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

type Filter struct {
	Source     string
	HazardType string
	Since      time.Time
	Limit      int
}

type Repository interface {
	Upsert(context.Context, []domain.HazardEvent) ([]domain.HazardEvent, error)
	List(context.Context, Filter) ([]domain.HazardEvent, error)
}

type MemoryRepository struct {
	mu      sync.RWMutex
	events  map[string]domain.HazardEvent
	order   []string
	maxRows int
}

func NewMemoryRepository(maxRows int) *MemoryRepository {
	if maxRows <= 0 {
		maxRows = 10000
	}
	return &MemoryRepository{
		events:  make(map[string]domain.HazardEvent),
		maxRows: maxRows,
	}
}

func (r *MemoryRepository) Upsert(_ context.Context, events []domain.HazardEvent) ([]domain.HazardEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inserted := make([]domain.HazardEvent, 0, len(events))
	for _, event := range events {
		if _, exists := r.events[event.HazardID]; !exists {
			inserted = append(inserted, event)
			r.order = append(r.order, event.HazardID)
		}
		r.events[event.HazardID] = event
	}
	for len(r.order) > r.maxRows {
		oldest := r.order[0]
		r.order[0] = ""
		r.order = r.order[1:]
		delete(r.events, oldest)
	}
	return inserted, nil
}

func (r *MemoryRepository) List(_ context.Context, filter Filter) ([]domain.HazardEvent, error) {
	r.mu.RLock()
	result := make([]domain.HazardEvent, 0, len(r.order))
	for _, id := range r.order {
		event, ok := r.events[id]
		if !ok {
			continue
		}
		if filter.Source != "" && event.Source != filter.Source {
			continue
		}
		if filter.HazardType != "" && event.HazardType != filter.HazardType {
			continue
		}
		if !filter.Since.IsZero() && event.OccurredAt.Before(filter.Since) {
			continue
		}
		result = append(result, event)
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].OccurredAt.After(result[j].OccurredAt) })
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}
