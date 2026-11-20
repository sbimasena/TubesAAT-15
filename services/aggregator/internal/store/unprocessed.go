package store

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

type UnprocessedItem struct {
	MessageID  string    `json:"message_id"`
	Source     string    `json:"source"`
	ReceivedAt time.Time `json:"received_at"`
	Reason     string    `json:"reason"`
	Retryable  bool      `json:"retryable"`
	Attempts   int       `json:"attempts"`
	Status     string    `json:"status"`
}

type UnprocessedPage struct {
	Items      []UnprocessedItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

func (r *Router) Unprocessed(ctx context.Context, source, cursor string, limit int) (UnprocessedPage, error) {
	var after string
	if cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil || len(raw) == 0 || len(raw) > 512 || !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
			return UnprocessedPage{}, ErrInvalidCursor
		}
		after = string(raw)
	}
	limit = clampLimit(limit)
	// DLQ data does not follow partition moves yet. Query all configured units,
	// not just current owners; fail instead of paginating over missing units.
	results := gather(ctx, r.units, unitReadTimeout, func(ctx context.Context, u *unit) ([]UnprocessedItem, error) {
		rows, err := u.repo.pool.Query(ctx, `SELECT message_id, source, received_at, reason, attempts, status
			FROM unprocessed_messages WHERE status IN ('parked', 'retrying')
			AND ($1 = '' OR source = $1) AND message_id COLLATE "C" > $2
			ORDER BY message_id COLLATE "C" LIMIT $3`, source, after, limit+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []UnprocessedItem
		for rows.Next() {
			var item UnprocessedItem
			if err := rows.Scan(&item.MessageID, &item.Source, &item.ReceivedAt, &item.Reason, &item.Attempts, &item.Status); err != nil {
				return nil, err
			}
			item.ReceivedAt = item.ReceivedAt.UTC()
			item.Retryable = deadletter.Retryable(strings.SplitN(item.Reason, ":", 2)[0])
			items = append(items, item)
		}
		return items, rows.Err()
	})
	byID := make(map[string]UnprocessedItem)
	for _, result := range results {
		if result.err != nil {
			return UnprocessedPage{}, result.err
		}
		for _, item := range result.value {
			if old, ok := byID[item.MessageID]; !ok || item.Attempts > old.Attempts {
				byID[item.MessageID] = item
			}
		}
	}
	page := UnprocessedPage{Items: make([]UnprocessedItem, 0, len(byID))}
	for _, item := range byID {
		page.Items = append(page.Items, item)
	}
	slices.SortFunc(page.Items, func(a, b UnprocessedItem) int { return strings.Compare(a.MessageID, b.MessageID) })
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := base64.RawURLEncoding.EncodeToString([]byte(page.Items[limit-1].MessageID))
		page.NextCursor = &next
	}
	return page, nil
}
