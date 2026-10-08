package httpapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy-stock/backend/internal/marketbars"
	"easy-stock/backend/internal/runtimelog"
)

const (
	marketAppendCronPath = "/config/stockstrategy-market-append.cron"
	marketAppendLogRoot  = "/logs/service"
	marketAppendHostLogs = "/mnt/4TB/dockerf/stockstrategy/logs/service"
	maxTaskLogTailBytes  = 48 * 1024
)

type liveMarketStatusSnapshot struct {
	Running         bool       `json:"running"`
	State           string     `json:"state"`
	SessionActive   bool       `json:"session_active"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	LastCheckAt     *time.Time `json:"last_check_at,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	IntervalMinutes int        `json:"interval_minutes"`
	IntervalReason  string     `json:"interval_reason,omitempty"`
	MemoryPercent   float64    `json:"memory_percent"`
	CPUPercent      float64    `json:"cpu_percent"`
	CPUKnown        bool       `json:"cpu_known"`
	LastRunStatus   string     `json:"last_run_status,omitempty"`
	Theme           string     `json:"theme,omitempty"`
	Signal          string     `json:"signal,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type liveMarketRuntimeState struct {
	mu       sync.RWMutex
	snapshot liveMarketStatusSnapshot
}

func newLiveMarketRuntimeState() *liveMarketRuntimeState {
	return &liveMarketRuntimeState{snapshot: liveMarketStatusSnapshot{State: "starting"}}
}

func (s *Server) updateLiveMarketStatus(update func(*liveMarketStatusSnapshot)) {
	if s == nil || s.liveMarketRuntime == nil || update == nil {
		return
	}
	s.liveMarketRuntime.mu.Lock()
	update(&s.liveMarketRuntime.snapshot)
	s.liveMarketRuntime.snapshot.UpdatedAt = time.Now()
	s.liveMarketRuntime.mu.Unlock()
}

func (s *Server) liveMarketStatus() liveMarketStatusSnapshot {
	if s == nil || s.liveMarketRuntime == nil {
		return liveMarketStatusSnapshot{State: "unavailable"}
	}
	s.liveMarketRuntime.mu.RLock()
	defer s.liveMarketRuntime.mu.RUnlock()
	return s.liveMarketRuntime.snapshot
}

type marketDataFreshness struct {
	Market       string `json:"market"`
	Rows         int64  `json:"rows"`
	LatestDate   string `json:"latest_date,omitempty"`
	ExpectedDate string `json:"expected_date,omitempty"`
	InvalidRows  int64  `json:"invalid_rows"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
}

type scheduledJobStatus struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Market       string     `json:"market"`
	Schedule     string     `json:"schedule"`
	Timezone     string     `json:"timezone"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	LastStatus   string     `json:"last_status"`
	LastMessage  string     `json:"last_message,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastStarted  *time.Time `json:"last_started_at,omitempty"`
	LastFinished *time.Time `json:"last_finished_at,omitempty"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	LogPath      string     `json:"log_path"`
	RecentLog    []string   `json:"recent_log,omitempty"`
}

type systemStatusResponse struct {
	GeneratedAt       time.Time                `json:"generated_at"`
	APIState          string                   `json:"api_state"`
	APIStartedAt      time.Time                `json:"api_started_at,omitempty"`
	APIUptimeSeconds  int64                    `json:"api_uptime_seconds"`
	DatabaseState     string                   `json:"database_state"`
	DatabaseError     string                   `json:"database_error,omitempty"`
	LiveMarket        liveMarketStatusSnapshot `json:"live_market"`
	MarketData        []marketDataFreshness    `json:"market_data"`
	ScheduledJobs     []scheduledJobStatus     `json:"scheduled_jobs"`
	ScheduleReadError string                   `json:"schedule_read_error,omitempty"`
}

type marketAppendSchedule struct {
	id       string
	name     string
	market   string
	cron     string
	timezone string
	logPath  string
	hostPath string
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	response := systemStatusResponse{
		GeneratedAt:   now,
		APIState:      "正常",
		DatabaseState: "正常",
		LiveMarket:    s.liveMarketStatus(),
	}
	if s != nil && !s.startedAt.IsZero() {
		response.APIStartedAt = s.startedAt
		response.APIUptimeSeconds = int64(now.Sub(s.startedAt).Seconds())
	}
	if s == nil || s.marketBars == nil {
		response.DatabaseState = "不可用"
		response.DatabaseError = "行情数据库未连接"
	} else {
		response.MarketData, response.DatabaseError = s.marketDataFreshness(r.Context(), now)
		if response.DatabaseError != "" {
			response.DatabaseState = "异常"
		}
	}
	response.ScheduledJobs, response.ScheduleReadError = loadMarketAppendStatuses(now)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) marketDataFreshness(ctx context.Context, now time.Time) ([]marketDataFreshness, string) {
	rows, err := s.marketBars.DailySummary(ctx)
	if err != nil {
		return nil, runtimelog.Redact(err.Error())
	}
	byMarket := make(map[string]marketbars.DailyMarketSummary, len(rows))
	for _, item := range rows {
		byMarket[item.Market] = item
	}
	result := make([]marketDataFreshness, 0, 3)
	for _, market := range []string{"CN", "US", "CRYPTO"} {
		item := byMarket[market]
		status := marketDataFreshness{Market: market, Rows: item.Rows, LatestDate: item.LatestDate, InvalidRows: item.InvalidRows, State: "缺失"}
		expected, calendarErr := latestCompletedDailyDate(market)
		if calendarErr != nil {
			status.State = "日历未知"
			status.Error = runtimelog.Redact(calendarErr.Error())
		} else {
			status.ExpectedDate = expected
			if item.InvalidRows > 0 {
				status.State = "异常数据"
			} else if item.Rows > 0 && item.LatestDate >= expected {
				status.State = "新鲜"
			} else if item.Rows > 0 {
				status.State = "待回补"
			}
		}
		result = append(result, status)
	}
	return result, ""
}

func loadMarketAppendStatuses(now time.Time) ([]scheduledJobStatus, string) {
	content, err := os.ReadFile(marketAppendCronPath)
	if err != nil {
		return nil, runtimelog.Redact("读取盘后任务计划失败：" + err.Error())
	}
	schedules := parseMarketAppendSchedules(string(content))
	result := make([]scheduledJobStatus, 0, len(schedules))
	for _, schedule := range schedules {
		status := scheduledJobStatus{
			ID: schedule.id, Name: schedule.name, Market: schedule.market,
			Schedule: schedule.cron, Timezone: schedule.timezone,
			LastStatus: "尚无记录", LogPath: schedule.hostPath,
		}
		if next, ok := nextCronOccurrence(schedule.cron, schedule.timezone, now); ok {
			status.NextRunAt = &next
		}
		if !readMarketAppendLog(&status, schedule.logPath, now) {
			status.LastMessage = "任务日志尚未生成，暂不能确认执行结果"
		}
		result = append(result, status)
	}
	if len(result) == 0 {
		return nil, "任务计划文件中未找到行情回补任务"
	}
	return result, ""
}

func parseMarketAppendSchedules(content string) []marketAppendSchedule {
	timezone := "Asia/Hong_Kong"
	labels := map[string]struct{ name, market string }{
		"cn":     {name: "A 股盘后行情回补", market: "A 股"},
		"us":     {name: "美股行情回补", market: "美股"},
		"crypto": {name: "币圈日线回补", market: "币圈"},
	}
	var result []marketAppendSchedule
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "CRON_TZ=") {
			timezone = strings.TrimSpace(strings.TrimPrefix(line, "CRON_TZ="))
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 7 || !strings.Contains(line, "market-append.sh") {
			continue
		}
		id := strings.ToLower(fields[len(fields)-1])
		label, ok := labels[id]
		if !ok {
			continue
		}
		logName := "market-append-" + id + ".log"
		result = append(result, marketAppendSchedule{
			id: id, name: label.name, market: label.market,
			cron: strings.Join(fields[:5], " "), timezone: timezone,
			logPath:  filepath.Join(marketAppendLogRoot, logName),
			hostPath: filepath.Join(marketAppendHostLogs, logName),
		})
	}
	return result
}

func nextCronOccurrence(expression, timezone string, now time.Time) (time.Time, bool) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return time.Time{}, false
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, false
	}
	start := now.In(location).Truncate(time.Minute).Add(time.Minute)
	for minute := 0; minute < 8*24*60; minute++ {
		candidate := start.Add(time.Duration(minute) * time.Minute)
		if !cronFieldMatches(fields[0], candidate.Minute(), 0, 59) ||
			!cronFieldMatches(fields[1], candidate.Hour(), 0, 23) ||
			!cronFieldMatches(fields[3], int(candidate.Month()), 1, 12) {
			continue
		}
		domMatch := cronFieldMatches(fields[2], candidate.Day(), 1, 31)
		dow := int(candidate.Weekday())
		dowMatch := cronFieldMatches(fields[4], dow, 0, 7) || (dow == 0 && cronFieldMatches(fields[4], 7, 0, 7))
		domWildcard, dowWildcard := isWildcardCronField(fields[2]), isWildcardCronField(fields[4])
		dayMatch := domMatch && dowMatch
		if !domWildcard && !dowWildcard {
			dayMatch = domMatch || dowMatch
		}
		if dayMatch {
			return candidate, true
		}
	}
	return time.Time{}, false
}

func isWildcardCronField(field string) bool {
	return field == "*" || strings.HasPrefix(field, "*/")
}

func cronFieldMatches(field string, value, minimum, maximum int) bool {
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			continue
		}
		step := 1
		base := part
		if pieces := strings.SplitN(part, "/", 2); len(pieces) == 2 {
			base = pieces[0]
			parsed, err := strconv.Atoi(pieces[1])
			if err != nil || parsed <= 0 {
				continue
			}
			step = parsed
		}
		start, end := minimum, maximum
		if base != "*" {
			if bounds := strings.SplitN(base, "-", 2); len(bounds) == 2 {
				var err error
				start, err = strconv.Atoi(bounds[0])
				if err != nil {
					continue
				}
				end, err = strconv.Atoi(bounds[1])
				if err != nil {
					continue
				}
			} else {
				parsed, err := strconv.Atoi(base)
				if err != nil {
					continue
				}
				start, end = parsed, parsed
			}
		}
		if value >= start && value <= end && (value-start)%step == 0 {
			return true
		}
	}
	return false
}

func readMarketAppendLog(status *scheduledJobStatus, path string, now time.Time) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	start := info.Size() - maxTaskLogTailBytes
	if start < 0 {
		start = 0
	}
	content, err := io.ReadAll(io.NewSectionReader(file, start, info.Size()-start))
	if err != nil {
		return false
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		status.RecentLog = append(status.RecentLog, runtimelog.Redact(truncateStatusText(line, 1000)))
	}

	contentLines := strings.Split(string(content), "\n")
	nonTradingSkip := false
	for _, line := range contentLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eventAt := parseAppendLogTime(line)
		if strings.Contains(line, "非交易日") && strings.Contains(line, "跳过") {
			nonTradingSkip = true
			status.LastMessage = "非交易日，任务按计划检查后跳过回补"
		}
		if strings.Contains(line, status.ID+" append started") {
			status.LastStatus = "运行中"
			status.LastStarted = timePointer(eventAt)
			status.LastFinished = nil
			status.ExitCode = nil
			status.LastError = ""
			nonTradingSkip = false
			status.LastMessage = "正在执行行情回补"
		} else if strings.Contains(line, status.ID+" append finished") {
			status.LastFinished = timePointer(eventAt)
			status.ExitCode = intPointer(0)
			if nonTradingSkip {
				status.LastStatus = "已跳过"
			} else if status.LastError != "" {
				status.LastStatus = "部分失败"
				status.LastMessage = "任务退出成功，但日志记录了批次或上游错误，请核对数据覆盖"
			} else {
				status.LastStatus = "成功"
				status.LastMessage = "行情回补任务正常结束"
			}
		} else if strings.Contains(line, status.ID+" append failed (exit=") {
			status.LastFinished = timePointer(eventAt)
			status.LastStatus = "失败"
			status.LastMessage = "行情回补失败，查看最近日志"
			code := parseAppendExitCode(line)
			status.ExitCode = &code
			status.LastError = runtimelog.Redact(truncateStatusText(line, 1000))
		} else if strings.Contains(line, "append already running; skipped") && strings.Contains(line, status.ID) {
			status.LastStatus = "已跳过"
			status.LastMessage = "已有同类回补任务运行，本次未重复启动"
			status.LastFinished = timePointer(eventAt)
		} else if strings.Contains(line, "crypto append completed with stale universe") && status.ID == "crypto" {
			status.LastStatus = "部分失败"
			status.LastMessage = "行情回补完成，但币种列表刷新失败"
			status.LastError = runtimelog.Redact(truncateStatusText(line, 1000))
			code := parseAppendExitCode(line)
			status.ExitCode = &code
			status.LastFinished = timePointer(eventAt)
		}
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "fail") || strings.Contains(lowerLine, "error") || strings.Contains(lowerLine, "失败") || strings.Contains(lowerLine, "错误") || strings.Contains(lowerLine, "traceback") {
			if status.LastStatus == "运行中" || status.LastStatus == "失败" || status.LastStatus == "部分失败" {
				status.LastError = runtimelog.Redact(truncateStatusText(line, 1000))
			}
		}
	}
	if status.LastStatus == "运行中" && status.LastStarted != nil && now.Sub(*status.LastStarted) > 2*time.Hour {
		status.LastStatus = "疑似中断"
		status.LastMessage = "日志记录任务已启动但长时间没有结束记录"
	}
	return true
}

func parseAppendLogTime(line string) time.Time {
	if !strings.HasPrefix(line, "[") {
		return time.Time{}
	}
	end := strings.IndexByte(line, ']')
	if end < 2 {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, line[1:end])
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func parseAppendExitCode(line string) int {
	index := strings.LastIndex(line, "exit=")
	if index < 0 {
		return -1
	}
	value := strings.TrimRight(line[index+len("exit="):], ") 	\r\n")
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func truncateStatusText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "…"
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func intPointer(value int) *int { return &value }
