package proxy

import (
	"context"
	"time"

	"uncodeapi/internal/config"
	"uncodeapi/internal/logger"
)

type Manager struct {
	cfg     *config.Config
	pool    *Pool
	fetcher *MultiFetcher
	stop    chan struct{}
}

func NewManager(cfg *config.Config, pool *Pool, fetcher *MultiFetcher) *Manager {
	return &Manager{
		cfg:     cfg,
		pool:    pool,
		fetcher: fetcher,
		stop:    make(chan struct{}),
	}
}

// Run loops: initial refresh + validate, then 24h periodic refresh or manual trigger.
func (m *Manager) Run(ctx context.Context) {
	m.Refresh(ctx)
	m.ValidateAll(ctx)

	refreshTick := time.NewTicker(m.cfg.ProxyRefreshInterval) // 24 Hours
	validateTick := time.NewTicker(30 * time.Minute)           // 30 Minutes periodic re-validation
	defer refreshTick.Stop()
	defer validateTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-refreshTick.C:
			logger.Info("24-hour scheduled proxy refresh starting...")
			m.Refresh(ctx)
			m.ValidateAll(ctx)
		case <-validateTick.C:
			m.ValidateAll(ctx)
		}
	}
}

func (m *Manager) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

func (m *Manager) Refresh(ctx context.Context) {
	logger.Info("refreshing proxy pool from %d sources...", len(m.fetcher.Fetchers))
	list := m.fetcher.Fetch(ctx)
	if len(list) == 0 {
		logger.Warn("no proxies fetched")
		return
	}
	m.pool.Set(list)
}

func (m *Manager) ValidateAll(ctx context.Context) {
	m.pool.ValidateAll(ctx)
}
