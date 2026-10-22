package portfolioinspection

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/hermes"
	"easy-stock/backend/internal/runtimelog"
	"easy-stock/backend/internal/stockanalysis"
)

var ErrJobRunning = errors.New("已有持仓巡检正在运行，请等待完成后再开始新的巡检")
var ErrDeleteRunningJob = errors.New("正在运行的巡检记录不能删除")
var ErrRuntimeUnavailable = errors.New("AI运行时不可用")

type StockAnalyzer func(context.Context, string) (stockanalysis.Analysis, error)
type HoldingAnalyzer func(context.Context, Holding) (stockanalysis.Analysis, error)
type DesktopAdapter struct {
	Validate       func(context.Context, Request) error
	AnalyzeHolding func(context.Context, Request, Holding) (stockanalysis.Analysis, error)
	GenerateReport func(context.Context, Request, string) (string, error)
}

type Service struct {
	store          *Store
	gateway        hermes.Gateway
	analyze        StockAnalyzer
	analyzeHolding HoldingAnalyzer
	logger         *log.Logger
	concurrency    int
	desktop        DesktopAdapter
	mu             sync.Mutex
	runningID      string
}

func (s *Service) SetDesktopAdapter(adapter DesktopAdapter) {
	if s != nil {
		s.desktop = adapter
	}
}

func NewService(store *Store, gateway hermes.Gateway, analyze StockAnalyzer, logger *log.Logger, holdingAnalyzers ...HoldingAnalyzer) *Service {
	service := &Service{store: store, gateway: gateway, analyze: analyze, logger: logger, concurrency: DefaultConcurrency}
	if len(holdingAnalyzers) > 0 {
		service.analyzeHolding = holdingAnalyzers[0]
	}
	if store != nil {
		_ = store.MarkInterrupted(context.Background())
	}
	return service
}

func (s *Service) Start(ctx context.Context, request Request) (Job, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Job{}, err
	}
	if s == nil || s.store == nil || s.analyze == nil {
		return Job{}, errors.New("持仓巡检服务不可用")
	}
	switch normalized.AIProvider {
	case "hermes":
		if s.gateway == nil {
			return Job{}, fmt.Errorf("%w：AI分析底座不可用，请在系统设置中配置 Hermes", ErrRuntimeUnavailable)
		}
		status := s.gateway.Status()
		if !status.Available || !status.Configured {
			return Job{}, fmt.Errorf("%w：%s", ErrRuntimeUnavailable, firstNonEmpty(status.Message, "请在系统设置中配置 Hermes"))
		}
	case "codex_desktop", "antigravity_desktop":
		if s.desktop.Validate == nil || s.desktop.AnalyzeHolding == nil || s.desktop.GenerateReport == nil {
			return Job{}, fmt.Errorf("%w：持仓巡检未接入所选桌面桥", ErrRuntimeUnavailable)
		}
		if err := s.desktop.Validate(ctx, normalized); err != nil {
			return Job{}, fmt.Errorf("%w：%v", ErrRuntimeUnavailable, err)
		}
	default:
		return Job{}, fmt.Errorf("不支持的 AI 运行时 %q", normalized.AIProvider)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningID != "" {
		return Job{}, ErrJobRunning
	}
	now := time.Now().UTC()
	job := Job{
		ID: newID(), Status: "running", Stage: "queued", Request: normalized,
		Results: make([]HoldingResult, len(normalized.Holdings)), TotalStocks: len(normalized.Holdings),
		Message: "任务已提交，将逐只完成个股分析后生成持仓总报告", StartedAt: now, UpdatedAt: now,
	}
	for index, holding := range normalized.Holdings {
		job.Results[index] = HoldingResult{Holding: holding, Status: "queued"}
	}
	if _, err := s.store.Save(ctx, job); err != nil {
		return Job{}, err
	}
	s.runningID = job.ID
	if s.logger != nil {
		s.logger.Printf("level=info event=portfolio_inspection_start feature=portfolio-inspection job_id=%q profile=%s stocks=%d total_position=%d", job.ID, normalized.TraderProfile, len(normalized.Holdings), totalPosition(normalized.Holdings))
	}
	initial := job
	initial.Results = append([]HoldingResult(nil), job.Results...)
	initial.Request.Holdings = append([]Holding(nil), job.Request.Holdings...)
	go s.run(job)
	return initial, nil
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	if s == nil || s.store == nil {
		return Job{}, errors.New("持仓巡检服务不可用")
	}
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, limit int) ([]Job, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("持仓巡检服务不可用")
	}
	return s.store.List(ctx, limit)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if s == nil || s.store == nil {
		return errors.New("持仓巡检服务不可用")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningID == id {
		return ErrDeleteRunningJob
	}
	job, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.Status == "running" || job.Status == "queued" {
		return ErrDeleteRunningJob
	}
	return s.store.Delete(ctx, id)
}

func (s *Service) run(job Job) {
	started := time.Now()
	defer func() {
		s.mu.Lock()
		if s.runningID == job.ID {
			s.runningID = ""
		}
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	type event struct {
		index    int
		started  bool
		analysis stockanalysis.Analysis
		err      error
	}
	work := make(chan int)
	events := make(chan event)
	workers := min(s.concurrency, len(job.Results))
	for worker := 0; worker < workers; worker++ {
		go func() {
			for index := range work {
				events <- event{index: index, started: true}
				var analysis stockanalysis.Analysis
				var err error
				if job.Request.AIProvider == "codex_desktop" || job.Request.AIProvider == "antigravity_desktop" {
					analysis, err = s.desktop.AnalyzeHolding(ctx, job.Request, job.Results[index].Holding)
				} else if s.analyzeHolding != nil {
					analysis, err = s.analyzeHolding(ctx, job.Results[index].Holding)
				} else {
					analysis, err = s.analyze(ctx, job.Results[index].Holding.Symbol)
				}
				events <- event{index: index, analysis: analysis, err: err}
			}
		}()
	}
	go func() {
		for index := range job.Results {
			work <- index
		}
		close(work)
	}()

	active := map[int]struct{}{}
	stockStartedAt := map[int]time.Time{}
	completed := 0
	for completed < len(job.Results) {
		item := <-events
		if item.started {
			active[item.index] = struct{}{}
			stockStartedAt[item.index] = time.Now()
			job.Results[item.index].Status = "running"
			job.Stage = "analyzing_stocks"
			job.Message = fmt.Sprintf("正在分析持仓个股，已完成 %d/%d", completed, len(job.Results))
			job.CurrentSymbols = activeSymbols(job.Results, active)
			s.persist(job)
			continue
		}
		delete(active, item.index)
		completed++
		job.Results[item.index].CompletedAt = time.Now().UTC()
		if item.err != nil {
			job.Results[item.index].Status = "failed"
			job.Results[item.index].Error = item.err.Error()
		} else {
			job.Results[item.index].Status = "succeeded"
			job.Results[item.index].Analysis = &item.analysis
			job.Results[item.index].Holding.Name = item.analysis.Name
			job.Request.Holdings[item.index].Name = item.analysis.Name
		}
		if s.logger != nil {
			status := job.Results[item.index].Status
			duration := time.Since(stockStartedAt[item.index]).Milliseconds()
			if item.err != nil {
				s.logger.Printf("level=warn event=portfolio_stock_analysis_complete feature=portfolio-inspection job_id=%q stock_index=%d status=%s duration_ms=%d error=%q", job.ID, item.index+1, status, duration, runtimelog.Redact(item.err.Error()))
			} else {
				s.logger.Printf("level=info event=portfolio_stock_analysis_complete feature=portfolio-inspection job_id=%q stock_index=%d status=%s duration_ms=%d", job.ID, item.index+1, status, duration)
			}
		}
		job.CompletedStocks = completed
		job.CoveragePercent = coverage(job.Results, job.Request)
		job.CurrentSymbols = activeSymbols(job.Results, active)
		job.Message = fmt.Sprintf("正在分析持仓个股，已完成 %d/%d", completed, len(job.Results))
		s.persist(job)
	}

	rules, _ := RulesFor(job.Request.TraderProfile)
	metrics := CalculateMetrics(job.Request, job.Results, rules)
	job.Stage = "aggregating"
	job.CurrentSymbols = nil
	job.CoveragePercent = metrics.CoveragePercent
	job.Message = "个股分析已完成，正在生成组合巡检报告"
	s.persist(job)
	if s.logger != nil {
		s.logger.Printf("level=info event=portfolio_aggregation_start feature=portfolio-inspection job_id=%q coverage=%.1f succeeded=%d total=%d", job.ID, metrics.CoveragePercent, succeededCount(job.Results), len(job.Results))
	}

	var baseline *PreviousInspectionBaseline
	auditBaseDate := ""
	if job.Request.PreviousInspectionID != "" {
		if prevJob, err := s.store.Get(ctx, job.Request.PreviousInspectionID); err == nil && prevJob.Report != nil {
			auditBaseDate = prevJob.Report.GeneratedAt.Format("2006-01-02 15:04")
			baseline = &PreviousInspectionBaseline{
				InspectionID:     prevJob.ID,
				GeneratedAt:      auditBaseDate,
				HealthScore:      prevJob.Report.Conclusion.HealthScore,
				RiskLevel:        prevJob.Report.Conclusion.RiskLevel,
				ExecutiveSummary: prevJob.Report.Conclusion.ExecutiveSummary,
				Holdings:         make(map[string]PreviousHoldingBaseline),
			}
			for _, h := range prevJob.Report.Conclusion.Holdings {
				baseline.Holdings[h.Symbol] = PreviousHoldingBaseline{
					Symbol:         h.Symbol,
					PortfolioRole:  h.PortfolioRole,
					ActionPriority: h.ActionPriority,
					Action:         h.Action,
					Confirmation:   h.Confirmation,
					Invalidation:   h.Invalidation,
					Conclusion:     h.Conclusion,
				}
			}
		}
	}

	conclusion := localReport(job.Request, job.Results, metrics, rules, baseline)
	aiErr := error(nil)
	researchCoverage := researchCoverageForAggregation(job.Results, metrics)
	if researchCoverage >= MinimumAICoverage {
		conclusion, aiErr = s.generateAIReport(ctx, job.Request, job.Results, metrics, rules, baseline)
	} else {
		aiErr = fmt.Errorf("有效个股AI研究仅覆盖 %.1f%% 持仓，低于组合结论所需的 %d%%；量化快照不算作AI研究成功", researchCoverage, MinimumAICoverage)
	}
	if aiErr != nil {
		conclusion = localReport(job.Request, job.Results, metrics, rules, baseline)
		conclusion.DataLimitations = append(conclusion.DataLimitations, aiErr.Error())
		if s.logger != nil {
			s.logger.Printf("level=warn event=portfolio_aggregation_degraded feature=portfolio-inspection job_id=%q error=%q", job.ID, runtimelog.Redact(aiErr.Error()))
		}
	}
	completedAt := time.Now().UTC()
	job.Report = &Report{
		ID:                   job.ID,
		PreviousInspectionID: job.Request.PreviousInspectionID,
		AuditBaseDate:        auditBaseDate,
		PromptVersion:        PromptVersion,
		AlgorithmVersion:     AlgorithmVersion,
		Profile:              rules,
		Holdings:             job.Results,
		Metrics:              metrics,
		Conclusion:           conclusion,
		GeneratedAt:          completedAt,
	}
	job.ReportAvailable = true
	job.Stage = "completed"
	job.UpdatedAt = completedAt
	job.CompletedAt = completedAt
	failedCount := len(job.Results) - succeededCount(job.Results)
	if failedCount == 0 && aiErr == nil {
		job.Status = "succeeded"
		job.Message = "持仓 AI 巡检已完成，报告已保存在本机"
		job.Error = ""
	} else {
		job.Status = "partial"
		job.Message = "巡检报告已生成，部分分析使用降级结果"
		job.Error = firstNonEmpty(errorText(aiErr), fmt.Sprintf("%d 只股票分析失败", failedCount))
	}
	s.persist(job)
	if s.logger != nil {
		s.logger.Printf("level=info event=portfolio_inspection_complete feature=portfolio-inspection job_id=%q status=%s stocks=%d coverage=%.1f duration_ms=%d", job.ID, job.Status, len(job.Results), metrics.CoveragePercent, time.Since(started).Milliseconds())
	}
}

func (s *Service) generateAIReport(ctx context.Context, request Request, results []HoldingResult, metrics Metrics, rules ProfileRules, baseline *PreviousInspectionBaseline) (AIReport, error) {
	prompt, err := buildPrompt(request, results, metrics, rules, baseline)
	if err != nil {
		return AIReport{}, err
	}
	var response hermes.PromptResult
	if request.AIProvider == "codex_desktop" || request.AIProvider == "antigravity_desktop" {
		content, err := s.desktop.GenerateReport(ctx, request, prompt)
		if err != nil {
			return AIReport{}, fmt.Errorf("持仓组合桌面AI分析失败: %w", err)
		}
		response.Content = content
	} else {
		var err error
		response, err = hermes.PromptFullyAuthorized(hermes.WithUsageModule(ctx, "portfolio-inspection"), s.gateway, prompt)
		if err != nil {
			return AIReport{}, fmt.Errorf("持仓组合AI分析失败: %w", err)
		}
	}
	var report AIReport
	if err := decodeJSONObject(response.Content, &report); err != nil {
		return AIReport{}, fmt.Errorf("持仓组合AI未返回有效JSON: %w", err)
	}
	if strings.TrimSpace(report.ExecutiveSummary) == "" || strings.TrimSpace(report.RiskLevel) == "" {
		return AIReport{}, errors.New("持仓组合AI返回缺少必要字段")
	}
	report.HealthScore = metrics.HealthScore
	report.RiskLevel = riskLevelForMetrics(metrics, rules)
	report.StyleMatch = styleMatchLabel(metrics.StyleMatchScore)
	report.Confidence = math.Max(0, math.Min(1, report.Confidence))
	report.Source = "hermes-ai"
	if request.AIProvider == "codex_desktop" {
		report.Source = "codex-desktop-ai"
	} else if request.AIProvider == "antigravity_desktop" {
		report.Source = "antigravity-desktop-ai"
	}
	report.PrimaryRisks = limitStrings(report.PrimaryRisks, 8)
	report.ConcentrationFinding = limitStrings(report.ConcentrationFinding, 8)
	report.AdjustmentOrder = limitStrings(report.AdjustmentOrder, 10)
	report.NextChecklist = limitStrings(report.NextChecklist, 10)
	report.DataLimitations = limitStrings(report.DataLimitations, 10)
	if len(report.Holdings) > len(request.Holdings) {
		report.Holdings = report.Holdings[:len(request.Holdings)]
	}
	if baseline != nil {
		if strings.TrimSpace(report.AuditSummary) == "" {
			report.AuditSummary = fmt.Sprintf("本次为对比 %s 前次巡检评估进行的走势审核更新：重点核验各股动作确认与失效条件，动态优化组合配置。", baseline.GeneratedAt)
		}
		for i := range report.Holdings {
			h := &report.Holdings[i]
			if prevH, ok := baseline.Holdings[h.Symbol]; ok {
				if h.PreviousAction == "" {
					h.PreviousAction = prevH.Action
				}
				if h.TrendAudit == "" {
					h.TrendAudit = fmt.Sprintf("对比前次建议【%s】：结合最新走势关注确认【%s】与失效【%s】", firstNonEmpty(prevH.Action, "持股观察"), firstNonEmpty(prevH.Confirmation, "站上均线"), firstNonEmpty(prevH.Invalidation, "跌破止损"))
				}
			}
		}
	}
	return report, nil
}

func normalizeRequest(request Request) (Request, error) {
	if _, ok := RulesFor(request.TraderProfile); !ok {
		return Request{}, errors.New("请选择有效的交易风格")
	}
	if len(request.Holdings) == 0 {
		return Request{}, errors.New("请至少添加一只持仓股票")
	}
	if len(request.Holdings) > MaxHoldings {
		return Request{}, fmt.Errorf("持仓股票最多支持 %d 只", MaxHoldings)
	}
	seen := map[string]struct{}{}
	total := 0
	provider := strings.TrimSpace(request.AIProvider)
	if provider == "" {
		provider = "hermes"
	}
	if provider != "hermes" && provider != "codex_desktop" && provider != "antigravity_desktop" {
		return Request{}, fmt.Errorf("ai_provider must be hermes, codex_desktop or antigravity_desktop")
	}
	prevID := strings.TrimSpace(request.PreviousInspectionID)
	if len(prevID) > 128 {
		return Request{}, errors.New("previous_inspection_id 长度不能超过 128")
	}
	normalized := Request{
		TraderProfile: request.TraderProfile, Holdings: make([]Holding, 0, len(request.Holdings)),
		AIProvider: provider, Model: strings.TrimSpace(request.Model), ReasoningEffort: strings.TrimSpace(request.ReasoningEffort),
		DesktopNodeID: strings.TrimSpace(request.DesktopNodeID), DesktopWorkspaceID: strings.TrimSpace(request.DesktopWorkspaceID),
		PreviousInspectionID: prevID,
	}
	if provider == "hermes" {
		if normalized.Model != "" || normalized.ReasoningEffort != "" || normalized.DesktopNodeID != "" || normalized.DesktopWorkspaceID != "" {
			return Request{}, errors.New("Hermes 请求不能包含桌面桥模型或节点")
		}
	} else if normalized.Model == "" || len(normalized.Model) > 128 || strings.ContainsAny(normalized.Model, "\r\n\x00") || normalized.ReasoningEffort == "" || len(normalized.ReasoningEffort) > 32 || strings.ContainsAny(normalized.ReasoningEffort, "\r\n\x00") || normalized.DesktopNodeID == "" || len(normalized.DesktopNodeID) > 128 || strings.ContainsAny(normalized.DesktopNodeID, "\r\n\x00") || normalized.DesktopWorkspaceID == "" || len(normalized.DesktopWorkspaceID) > 128 || strings.ContainsAny(normalized.DesktopWorkspaceID, "\r\n\x00") {
		return Request{}, fmt.Errorf("%s requires a valid model, reasoning_effort and desktop node", provider)
	}
	for _, holding := range request.Holdings {
		symbol, err := foundation.NormalizeSymbol(holding.Symbol)
		if err != nil {
			return Request{}, fmt.Errorf("无效股票代码 %q: %w", holding.Symbol, err)
		}
		if _, exists := seen[symbol.Canonical]; exists {
			return Request{}, fmt.Errorf("股票 %s 重复添加", symbol.Canonical)
		}
		if holding.Weight <= 0 || holding.Weight > 100 {
			return Request{}, fmt.Errorf("%s 的持仓占比必须在 1%% 到 100%% 之间", symbol.Canonical)
		}
		if holding.CostPrice != nil && *holding.CostPrice <= 0 {
			return Request{}, fmt.Errorf("%s 的持仓成本必须大于 0", symbol.Canonical)
		}
		total += holding.Weight
		seen[symbol.Canonical] = struct{}{}
		holding.Symbol = symbol.Canonical
		holding.Name = strings.TrimSpace(holding.Name)
		normalized.Holdings = append(normalized.Holdings, holding)
	}
	if total > 100 {
		return Request{}, errors.New("持仓总占比不能超过 100%")
	}
	return normalized, nil
}

func activeSymbols(results []HoldingResult, active map[int]struct{}) []string {
	symbols := make([]string, 0, len(active))
	for index := range active {
		symbols = append(symbols, results[index].Holding.Symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func coverage(results []HoldingResult, request Request) float64 {
	total, succeeded := 0, 0
	for index, holding := range request.Holdings {
		total += holding.Weight
		if index < len(results) && results[index].Status == "succeeded" {
			succeeded += holding.Weight
		}
	}
	if total == 0 {
		return 0
	}
	return round(float64(succeeded)/float64(total)*100, 1)
}

func totalPosition(holdings []Holding) int {
	total := 0
	for _, holding := range holdings {
		total += holding.Weight
	}
	return total
}

func succeededCount(results []HoldingResult) int {
	count := 0
	for _, result := range results {
		if result.Status == "succeeded" {
			count++
		}
	}
	return count
}

func (s *Service) persist(job Job) {
	job.UpdatedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.store.Save(ctx, job); err != nil && s.logger != nil {
		s.logger.Printf("level=warn event=portfolio_inspection_persist_error feature=portfolio-inspection job_id=%q error=%q", job.ID, runtimelog.Redact(err.Error()))
	}
}

func newID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("portfolio-%d", time.Now().UnixNano())
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func decodeJSONObject(content string, target any) error {
	content = strings.TrimSpace(content)
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return errors.New("JSON object not found")
	}
	return json.Unmarshal([]byte(content[start:end+1]), target)
}

func limitStrings(values []string, limit int) []string {
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
		if len(result) >= limit {
			break
		}
	}
	return result
}
