package store

import (
	"context"
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
	Upsert(context.Context, []domain.HazardEvent, string) ([]domain.HazardEvent, error)
	List(context.Context, Filter, string) ([]domain.HazardEvent, error)
	Ping(context.Context) error
}
