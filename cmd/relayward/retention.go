package main

import (
	"context"
	"log/slog"
	"time"

	"relayward-mail/internal/store"
)

// retentionInterval is how often old message log rows are pruned.
const retentionInterval = 24 * time.Hour

// runRetention deletes message log rows older than retentionDays, once at
// startup and then every day, until ctx is done. A non-positive retentionDays
// keeps everything.
func runRetention(ctx context.Context, st *store.Store, retentionDays int, logger *slog.Logger) error {
	if retentionDays <= 0 {
		return nil
	}
	prune := func() {
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		pruneCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n, err := st.PruneMessages(pruneCtx, cutoff)
		if err != nil {
			logger.Error("prune message log", slog.String("err", err.Error()))
			return
		}
		logger.Info("message log pruned", slog.Int64("deleted", n), slog.Int("retention_days", retentionDays))
	}

	prune()
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			prune()
		}
	}
}
