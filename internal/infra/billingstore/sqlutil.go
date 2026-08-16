package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n * 2)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	return b.String()
}

func sqlInArgs(values []string) ([]any, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("billingstore: empty IN list")
	}
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args, nil
}

func waitContention(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func requireRowsAffected(result sql.Result, want int64, what string) error {
	if result == nil {
		return fmt.Errorf("billingstore: %s: nil result", what)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: %s rows affected: %w", what, err)
	}
	if count != want {
		return fmt.Errorf("billingstore: %s: expected %d row(s), got %d", what, want, count)
	}
	return nil
}
