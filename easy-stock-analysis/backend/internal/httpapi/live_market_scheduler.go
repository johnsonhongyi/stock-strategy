package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"easy-stock/backend/internal/runtimelog"
	"easy-stock/backend/internal/tradingcalendar"
)

const (
	liveMarketCheckInterval  = time.Minute
	liveMarketRefreshTimeout = 55 * time.Second
)

type liveResourceSample struct {
	memoryPercent float64
	cpuPercent    float64
	cpuKnown      bool
}

type liveResourceMonitor struct {
	lastCPUUsec uint64
	lastAt      time.Time
}

type liveMarketRunResult struct {
	theme        string
	signal       string
	themeFresh   bool
	signalFresh  bool
	rateLimited  bool
	partialError string
}

func (s *Server) RunLiveMarketScheduler(ctx context.Context) {
	if s == nil {
		return
	}
	startedAt := time.Now()
	s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
		status.Running = true
		status.State = "waiting-session"
		status.StartedAt = timePointer(startedAt)
		status.LastError = ""
	})
	defer s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
		status.Running = false
		status.SessionActive = false
		status.State = "stopped"
		status.NextRunAt = nil
	})
	if s.logger != nil {
		s.logger.Printf("level=info event=scheduler_start feature=live-market task=adaptive-refresh")
		defer s.logger.Printf("level=info event=scheduler_stop feature=live-market task=adaptive-refresh")
	}
	monitor := liveResourceMonitor{}
	monitor.sample(time.Now())
	state := liveMarketScheduleState{}
	ticker := time.NewTicker(liveMarketCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		sample := monitor.sample(now)
		s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
			status.LastCheckAt = timePointer(now)
			status.MemoryPercent = sample.memoryPercent
			status.CPUPercent = sample.cpuPercent
			status.CPUKnown = sample.cpuKnown
		})
		inSession, err := tradingcalendar.IsTradingSession("CN", now)
		if err != nil {
			s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
				status.SessionActive = false
				status.State = "calendar-error"
				status.IntervalReason = "trading-calendar-unavailable"
				status.NextRunAt = nil
				status.LastError = runtimelog.Redact(err.Error())
			})
			s.logLiveSchedulerError("trading-calendar", err)
			continue
		}
		if !inSession {
			s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
				status.SessionActive = false
				status.State = "waiting-session"
				status.IntervalReason = "outside-trading-session"
				status.NextRunAt = nil
			})
			continue
		}

		interval, reason := state.intervalFor(sample, now)
		if interval != state.interval {
			state.interval = interval
			s.setThemeRefreshInterval(interval)
			if s.marketEmotionIntraday != nil {
				s.marketEmotionIntraday.setTTL(interval)
			}
			if s.logger != nil {
				s.logger.Printf("level=info event=live_refresh_interval feature=live-market minutes=%d reason=%s memory_pct=%.1f cpu_pct=%.1f",
					int(interval/time.Minute), reason, sample.memoryPercent, sample.cpuPercent)
			}
			if !state.lastRun.IsZero() {
				state.nextRun = state.lastRun.Add(interval)
			}
		}
		s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
			status.SessionActive = true
			status.MemoryPercent = sample.memoryPercent
			status.CPUPercent = sample.cpuPercent
			status.CPUKnown = sample.cpuKnown
			status.IntervalMinutes = int(interval / time.Minute)
			status.IntervalReason = reason
			status.NextRunAt = timePointer(state.nextRun)
		})
		if !state.nextRun.IsZero() && now.Before(state.nextRun) {
			s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
				status.State = "scheduled"
			})
			continue
		}

		s.updateLiveMarketStatus(func(status *liveMarketStatusSnapshot) {
			status.State = "running"
			status.NextRunAt = nil
		})
		runCtx, cancel := context.WithTimeout(ctx, liveMarketRefreshTimeout)
		result := s.refreshLiveMarket(runCtx)
		cancel()
		finished := time.Now()
		state.lastRun = finished
		if result.rateLimited {
			state.rateLimitedUntil = finished.Add(time.Hour)
			state.failures = 0
		} else if !result.themeFresh || !result.signalFresh {
			state.failures = min(state.failures+1, 3)
		} else {
			state.failures = 0
			state.rateLimitedUntil = time.Time{}
		}
		afterInterval, afterReason := state.intervalFor(sample, finished)
		if afterInterval != state.interval {
			state.interval = afterInterval
			s.setThemeRefreshInterval(afterInterval)
			if s.marketEmotionIntraday != nil {
				s.marketEmotionIntraday.setTTL(afterInterval)
			}
		}
		state.nextRun = finished.Add(state.interval)
		status := "ok"
		if !result.themeFresh || !result.signalFresh || result.partialError != "" {
			status = "partial"
		}
		if result.rateLimited {
			status = "rate-limited"
		}
		s.updateLiveMarketStatus(func(snapshot *liveMarketStatusSnapshot) {
			snapshot.State = status
			snapshot.SessionActive = true
			snapshot.LastCheckAt = timePointer(finished)
			snapshot.LastRunAt = timePointer(finished)
			snapshot.NextRunAt = timePointer(state.nextRun)
			snapshot.IntervalMinutes = int(state.interval / time.Minute)
			snapshot.IntervalReason = afterReason
			snapshot.MemoryPercent = sample.memoryPercent
			snapshot.CPUPercent = sample.cpuPercent
			snapshot.CPUKnown = sample.cpuKnown
			snapshot.LastRunStatus = status
			snapshot.Theme = result.theme
			snapshot.Signal = result.signal
			snapshot.LastError = runtimelog.Redact(result.partialError)
		})
		s.logLiveSchedulerRun(result, state.interval, afterReason, sample)
	}
}

type liveMarketScheduleState struct {
	interval         time.Duration
	nextRun          time.Time
	lastRun          time.Time
	rateLimitedUntil time.Time
	failures         int
}

func (s liveMarketScheduleState) intervalFor(sample liveResourceSample, now time.Time) (time.Duration, string) {
	if now.Before(s.rateLimitedUntil) {
		return time.Hour, "upstream-rate-limit-cooldown"
	}
	interval := 15 * time.Minute
	reason := "resources-normal"
	switch {
	case sample.memoryPercent >= 85 || (sample.cpuKnown && sample.cpuPercent >= 75):
		interval, reason = time.Hour, "resource-pressure-high"
	case sample.memoryPercent >= 70 || (sample.cpuKnown && sample.cpuPercent >= 55):
		interval, reason = 45*time.Minute, "resource-pressure-medium-high"
	case sample.memoryPercent >= 55 || (sample.cpuKnown && sample.cpuPercent >= 35):
		interval, reason = 30*time.Minute, "resource-pressure-medium"
	}
	for i := 0; i < s.failures; i++ {
		interval = slowerLiveInterval(interval)
	}
	if s.failures > 0 {
		reason = "upstream-or-signal-errors"
	}
	return interval, reason
}

func slowerLiveInterval(interval time.Duration) time.Duration {
	switch interval {
	case 15 * time.Minute:
		return 30 * time.Minute
	case 30 * time.Minute:
		return 45 * time.Minute
	default:
		return time.Hour
	}
}

func (m *liveResourceMonitor) sample(now time.Time) liveResourceSample {
	sample := liveResourceSample{memoryPercent: readMemoryPercent()}
	usage, usageOK := readCPUUsageUsec()
	if usageOK && !m.lastAt.IsZero() && now.After(m.lastAt) && usage >= m.lastCPUUsec {
		quota := readCPUQuotaCores()
		elapsed := now.Sub(m.lastAt).Microseconds()
		if elapsed > 0 && quota > 0 {
			sample.cpuPercent = float64(usage-m.lastCPUUsec) / float64(elapsed) / quota * 100
			if sample.cpuPercent > 100 {
				sample.cpuPercent = 100
			}
			sample.cpuKnown = true
		}
	}
	if usageOK {
		m.lastCPUUsec = usage
		m.lastAt = now
	}
	return sample
}

func readMemoryPercent() float64 {
	if current, currentOK := readUintFile("/sys/fs/cgroup/memory.current"); currentOK {
		if limit, limitOK := readUintFile("/sys/fs/cgroup/memory.max"); limitOK && limit > 0 && limit < 1<<60 && current <= limit {
			return float64(current) / float64(limit) * 100
		}
	}
	if current, currentOK := readUintFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); currentOK {
		if limit, limitOK := readUintFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); limitOK && limit > 0 && limit < 1<<60 && current <= limit {
			return float64(current) / float64(limit) * 100
		}
	}
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	var total, available float64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total = value
		case "MemAvailable":
			available = value
		}
	}
	if total <= 0 || available < 0 || available > total {
		return 0
	}
	return (total - available) / total * 100
}

func readCPUUsageUsec() (uint64, bool) {
	if contents, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
		for _, line := range strings.Split(string(contents), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "usage_usec" {
				value, err := strconv.ParseUint(fields[1], 10, 64)
				return value, err == nil
			}
		}
	}
	if nanoseconds, ok := readUintFile("/sys/fs/cgroup/cpuacct/cpuacct.usage"); ok {
		return nanoseconds / 1000, true
	}
	return 0, false
}

func readCPUQuotaCores() float64 {
	if contents, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(contents))
		if len(fields) == 2 && fields[0] != "max" {
			quota, qErr := strconv.ParseFloat(fields[0], 64)
			period, pErr := strconv.ParseFloat(fields[1], 64)
			if qErr == nil && pErr == nil && quota > 0 && period > 0 {
				return quota / period
			}
		}
		return float64(max(runtime.GOMAXPROCS(0), 1))
	}
	quota, qErr := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, pErr := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if qErr == nil && pErr == nil && quota > 0 && period > 0 {
		return float64(quota) / float64(period)
	}
	return float64(max(runtime.GOMAXPROCS(0), 1))
}

func readUintFile(path string) (uint64, bool) {
	value, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(string(value)), 10, 64)
	return parsed, err == nil
}

func readIntFile(path string) (int64, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
}

func (s *Server) refreshLiveMarket(ctx context.Context) liveMarketRunResult {
	result := liveMarketRunResult{}
	if s.themeOverview != nil {
		progress, started := s.refreshThemeProgressNow(ctx)
		if !started && progress.Refreshing {
			result.theme = "another-refresh-in-progress"
		} else {
			result.themeFresh = len(progress.Data) > 0 && hasReadyThemeStage(progress.Steps)
			if result.themeFresh {
				result.theme = fmt.Sprintf("themes=%d stage=%s", len(progress.Data), progress.Stage)
			} else {
				result.theme = "no-fresh-theme-stage"
			}
			for _, message := range progress.Errors {
				result.partialError = appendLiveError(result.partialError, message)
				result.rateLimited = result.rateLimited || isLiveRateLimit(message)
			}
		}
	} else {
		result.theme = "theme-provider-unavailable"
	}

	if s.marketEmotion != nil && s.marketEmotionIntraday != nil {
		history, err := s.marketEmotion.readLocal(ctx)
		if err == nil {
			signal, signalErr := s.refreshMarketEmotionIntraday(ctx, history.Latest)
			result.signalFresh = signalErr == nil && !signal.Stale && !signal.UpdatedAt.IsZero()
			if result.signalFresh {
				result.signal = fmt.Sprintf("risk=%.0f status=%s", signal.RiskScore, signal.Status)
			} else {
				result.signal = "intraday-signal-stale-or-unavailable"
			}
			if signalErr != nil {
				result.partialError = appendLiveError(result.partialError, signalErr.Error())
				result.rateLimited = result.rateLimited || isLiveRateLimit(signalErr.Error())
			}
			if signal.Stale {
				if staleErr := s.marketEmotionIntraday.lastError(); staleErr != nil {
					result.partialError = appendLiveError(result.partialError, staleErr.Error())
					result.rateLimited = result.rateLimited || isLiveRateLimit(staleErr.Error())
				}
			}
		} else {
			result.signal = "market-emotion-local-read-failed"
			result.partialError = appendLiveError(result.partialError, err.Error())
		}
	} else {
		result.signal = "intraday-signal-service-unavailable"
	}
	return result
}

func hasReadyThemeStage(steps map[string]string) bool {
	for _, status := range steps {
		if status == "ready" {
			return true
		}
	}
	return false
}

func isLiveRateLimit(message string) bool {
	value := strings.ToLower(message)
	for _, marker := range []string{"429", "too many requests", "rate limit", "rate-limit", "限流", "请求过于频繁", "访问频率"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func appendLiveError(existing, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return existing
	}
	if existing == "" {
		return message
	}
	return existing + "; " + message
}

func (s *Server) logLiveSchedulerRun(result liveMarketRunResult, interval time.Duration, reason string, sample liveResourceSample) {
	if s.logger == nil {
		return
	}
	status := "ok"
	if !result.themeFresh || !result.signalFresh || result.partialError != "" {
		status = "partial"
	}
	if result.rateLimited {
		status = "rate-limited"
	}
	s.logger.Printf("level=info event=scheduler_run feature=live-market task=adaptive-refresh status=%s interval_minutes=%d reason=%s memory_pct=%.1f cpu_pct=%.1f theme=%q signal=%q error=%q",
		status, int(interval/time.Minute), reason, sample.memoryPercent, sample.cpuPercent, result.theme, result.signal, runtimelog.Redact(result.partialError))
}

func (s *Server) logLiveSchedulerError(task string, err error) {
	if s.logger != nil && err != nil {
		s.logger.Printf("level=warn event=scheduler_error feature=live-market task=%s error=%q", task, runtimelog.Redact(err.Error()))
	}
}
