package httpapi

import (
	"context"
	"sync"
	"time"
)

// Slow disk-wide diagnostics belong to one background read, never the UI request.
// CheckedAt describes the completed observation; GeneratedAt is only HTTP time.
type systemStatusCache struct {
	mu              sync.Mutex
	wg              sync.WaitGroup
	loading, closed bool
	checkedAt       time.Time
	attemptedAt     time.Time
	rows            []marketDataFreshness
	errorMessage    string
	cancel          context.CancelFunc
}

func (s *Server) cachedMarketDataFreshness(now time.Time) ([]marketDataFreshness, string, *time.Time, bool) {
	c := &s.marketStatusCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, "服务正在关闭", nil, false
	}
	if !c.loading && (c.attemptedAt.IsZero() || now.Sub(c.attemptedAt) >= time.Minute) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		c.loading, c.cancel = true, cancel
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			defer cancel()
			rows, message := s.marketDataFreshness(ctx, time.Now())
			c.mu.Lock()
			defer c.mu.Unlock()
			if !c.closed {
				if message == "" {
					c.rows = rows
					c.checkedAt = time.Now()
				}
				c.errorMessage = message
				c.attemptedAt = time.Now()
			}
			c.loading = false
			c.cancel = nil
		}()
	}
	var checked *time.Time
	if !c.checkedAt.IsZero() {
		value := c.checkedAt
		checked = &value
	}
	rows := append([]marketDataFreshness{}, c.rows...)
	return rows, c.errorMessage, checked, c.loading
}

func (s *Server) closeSystemStatusCache() {
	c := &s.marketStatusCache
	c.mu.Lock()
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
}
