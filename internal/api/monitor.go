package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"relayward-mail/internal/relay"
)

// UpstreamMonitor probes the upstream provider in the background so that
// /healthz can report connectivity without dialing the provider per request.
type UpstreamMonitor struct {
	client   *relay.Client
	interval time.Duration
	logger   *slog.Logger

	mu      sync.Mutex
	healthy bool
}

// NewUpstreamMonitor builds a monitor that probes every interval.
func NewUpstreamMonitor(client *relay.Client, interval time.Duration, logger *slog.Logger) *UpstreamMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpstreamMonitor{client: client, interval: interval, logger: logger}
}

// Run probes immediately and then on every tick until ctx is done.
func (m *UpstreamMonitor) Run(ctx context.Context) error {
	m.probe(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.probe(ctx)
		}
	}
}

func (m *UpstreamMonitor) probe(ctx context.Context) {
	err := m.client.Probe(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.healthy = true
		return
	}
	m.healthy = false
	m.logger.Warn("upstream probe failed", slog.String("err", err.Error()))
}

// Healthy reports the result of the most recent probe.
func (m *UpstreamMonitor) Healthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthy
}
