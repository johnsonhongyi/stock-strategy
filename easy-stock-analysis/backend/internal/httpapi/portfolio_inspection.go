package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"easy-stock/backend/internal/hermes"
	"easy-stock/backend/internal/portfolioinspection"
	"easy-stock/backend/internal/stockanalysis"
)

func (s *Server) portfolioStockResearchRequest(request portfolioinspection.Request, holding portfolioinspection.Holding) stockanalysis.ResearchRequest {
	return stockanalysis.ResearchRequest{
		Symbol: holding.Symbol, Purpose: "holding", Horizon: "swing", CostPrice: holding.CostPrice,
		AnalysisLevel: stockanalysis.ResearchLevelStandard, AIProvider: request.AIProvider,
		Model: request.Model, ReasoningEffort: request.ReasoningEffort,
		DesktopNodeID: request.DesktopNodeID, DesktopWorkspaceID: request.DesktopWorkspaceID,
	}
}

func (s *Server) validatePortfolioDesktopRuntime(ctx context.Context, request portfolioinspection.Request) error {
	if len(request.Holdings) == 0 {
		return errors.New("请至少添加一只持仓股票")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	_, err := s.stockResearchCodexIdentity(s.portfolioStockResearchRequest(request, request.Holdings[0]))
	return err
}

func (s *Server) analyzePortfolioHolding(ctx context.Context, request portfolioinspection.Request, holding portfolioinspection.Holding) (stockanalysis.Analysis, error) {
	return s.awaitStockResearch(ctx, s.portfolioStockResearchRequest(request, holding))
}

func (s *Server) generatePortfolioDesktopReport(ctx context.Context, request portfolioinspection.Request, prompt string) (string, error) {
	if len(request.Holdings) == 0 {
		return "", errors.New("组合没有可分析的持仓")
	}
	prompter, err := s.newStockResearchCodexPrompter(s.portfolioStockResearchRequest(request, request.Holdings[0]), "portfolio-inspection")
	if err != nil {
		return "", err
	}
	defer prompter.Close()
	result, err := prompter.PromptWithOptions(ctx, prompt, hermes.PromptOptions{Sandbox: true, AutoApprove: true, DisableTools: true})
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func (s *Server) portfolioInspectionCreate(w http.ResponseWriter, r *http.Request) {
	if s.portfolioInspection == nil {
		writeError(w, http.StatusServiceUnavailable, "持仓巡检服务不可用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request portfolioinspection.Request
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	job, err := s.portfolioInspection.Start(r.Context(), request)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, portfolioinspection.ErrJobRunning) {
			status = http.StatusConflict
		} else if errors.Is(err, portfolioinspection.ErrRuntimeUnavailable) || strings.Contains(err.Error(), "AI") || strings.Contains(err.Error(), "模型") {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": job})
}

func (s *Server) portfolioInspectionGet(w http.ResponseWriter, r *http.Request) {
	if s.portfolioInspection == nil {
		writeError(w, http.StatusServiceUnavailable, "持仓巡检服务不可用")
		return
	}
	job, err := s.portfolioInspection.Get(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "未找到持仓巡检任务")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": job})
}

func (s *Server) portfolioInspectionList(w http.ResponseWriter, r *http.Request) {
	if s.portfolioInspection == nil {
		writeError(w, http.StatusServiceUnavailable, "持仓巡检服务不可用")
		return
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 30 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 30")
			return
		}
		limit = parsed
	}
	jobs, err := s.portfolioInspection.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": jobs})
}
