package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"easy-stock/backend/internal/hermes"
	"easy-stock/backend/internal/stockanalysis"
)

const stockResearchCodexPurpose = "stock_research"

type stockResearchCodexPrompter struct {
	server    *Server
	db        *sql.DB
	request   stockanalysis.ResearchRequest
	model     string
	effort    string
	node      string
	workspace string
	owner     string
	session   string
	thread    string
	identity  string
}

func (s *Server) stockResearchCodexIdentity(request stockanalysis.ResearchRequest) (string, error) {
	db, err := s.chatDB()
	if err != nil {
		return "", errors.New("Codex Desktop 运行时不可用")
	}
	defer db.Close()
	cfg := loadDesktopChatConfig(db)
	models, online := desktopChatCatalog(db, cfg)
	if !online || s.linkage == nil || !s.linkage.IsConfiguredTarget(cfg.Workspace, cfg.Node) {
		return "", errors.New("Codex Desktop 桌面节点未连接")
	}
	selection := cfg
	selection.Model, selection.Effort = request.Model, request.ReasoningEffort
	if !validDesktopSelection(selection, models) {
		return "", errors.New("所选 Codex 模型或思考强度已不受当前桌面支持")
	}
	identity, _ := json.Marshal([]string{"codex_desktop", cfg.Node, cfg.Workspace, request.Model, request.ReasoningEffort})
	return string(identity), nil
}

func (s *Server) newStockResearchCodexPrompter(request stockanalysis.ResearchRequest) (*stockResearchCodexPrompter, error) {
	db, err := s.chatDB()
	if err != nil {
		return nil, errors.New("Codex Desktop 对话队列不可用")
	}
	cfg := loadDesktopChatConfig(db)
	models, online := desktopChatCatalog(db, cfg)
	if !online || s.linkage == nil || !s.linkage.IsConfiguredTarget(cfg.Workspace, cfg.Node) {
		db.Close()
		return nil, errors.New("Codex Desktop 桌面节点未连接")
	}
	selection := cfg
	selection.Model, selection.Effort = request.Model, request.ReasoningEffort
	if !validDesktopSelection(selection, models) {
		db.Close()
		return nil, errors.New("所选 Codex 模型或思考强度已不受当前桌面支持")
	}
	session := chatID()
	if session == "" {
		db.Close()
		return nil, errors.New("Codex Desktop 研究会话创建失败")
	}
	owner := "stock-research-" + session
	if _, err = db.Exec("INSERT INTO desktop_chat_sessions(id,owner,node_id,workspace_id,model,effort) VALUES(?,?,?,?,?,?)", session, owner, cfg.Node, cfg.Workspace, request.Model, request.ReasoningEffort); err != nil {
		db.Close()
		return nil, errors.New("Codex Desktop 研究会话保存失败")
	}
	identity, _ := json.Marshal([]string{"codex_desktop", cfg.Node, cfg.Workspace, request.Model, request.ReasoningEffort})
	return &stockResearchCodexPrompter{
		server: s, db: db, request: request, model: request.Model, effort: request.ReasoningEffort,
		node: cfg.Node, workspace: cfg.Workspace, owner: owner, session: session, identity: string(identity),
	}, nil
}

func (p *stockResearchCodexPrompter) Prompt(ctx context.Context, prompt string) (hermes.PromptResult, error) {
	return p.PromptWithOptions(ctx, prompt, hermes.PromptOptions{Sandbox: true, AutoApprove: true, DisableTools: true})
}

func (p *stockResearchCodexPrompter) PromptWithOptions(ctx context.Context, prompt string, options hermes.PromptOptions) (hermes.PromptResult, error) {
	if !options.DisableTools {
		return hermes.PromptResult{}, errors.New("Codex Desktop 个股研究只允许使用冻结证据，不启用工具")
	}
	if strings.TrimSpace(prompt) == "" {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究提示为空")
	}
	if identity, err := p.server.stockResearchCodexIdentity(p.request); err != nil || identity != p.identity {
		return hermes.PromptResult{}, errors.New("Codex Desktop 模型或桌面节点配置已变化，请重新开始研究")
	}
	jobID := chatID()
	if jobID == "" {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究任务创建失败")
	}
	body, err := json.Marshal(map[string]any{
		"prompt": prompt, "messages": []any{}, "thread_id": p.thread,
		"model": p.model, "reasoning_effort": p.effort, "purpose": stockResearchCodexPurpose,
		"symbol": p.request.Symbol, "analysis_level": p.request.AnalysisLevel,
		"timeout_seconds": int(stockanalysis.ResearchStageTimeout(p.request).Seconds()),
	})
	if err != nil {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究任务编码失败")
	}
	now := time.Now().Unix()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究队列繁忙")
	}
	defer tx.Rollback()
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM desktop_chat_jobs WHERE node_id=? AND status IN ('queued','running') AND expires_at>?", p.node, now).Scan(&pending); err != nil || pending >= 8 {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究队列已满")
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM desktop_chat_jobs WHERE session_id=? AND status IN ('queued','running') AND expires_at>?", p.session, now).Scan(&pending); err != nil || pending > 0 {
		return hermes.PromptResult{}, errors.New("当前 Codex Desktop 研究阶段仍在运行")
	}
	expiresAt := now + int64(stockanalysis.ResearchStageTimeout(p.request).Seconds()) + 120
	if expiresAt < now+600 {
		expiresAt = now + 600
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at) VALUES(?,?,?,?,?,?,'queued',?,?)", jobID, p.session, p.owner, p.node, p.workspace, string(body), now, expiresAt); err != nil {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究任务入队失败")
	}
	if err = tx.Commit(); err != nil {
		return hermes.PromptResult{}, errors.New("Codex Desktop 研究任务提交失败")
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			_, _ = p.db.Exec("UPDATE desktop_chat_jobs SET status='cancelled' WHERE id=? AND owner=? AND status IN ('queued','running')", jobID, p.owner)
			return hermes.PromptResult{}, ctx.Err()
		}
		var status, raw string
		var expires int64
		err = p.db.QueryRowContext(ctx, "SELECT status,result,expires_at FROM desktop_chat_jobs WHERE id=? AND owner=?", jobID, p.owner).Scan(&status, &raw, &expires)
		if err != nil {
			if ctx.Err() != nil {
				return hermes.PromptResult{}, ctx.Err()
			}
			return hermes.PromptResult{}, errors.New("Codex Desktop 研究任务状态不可读")
		}
		if expires <= time.Now().Unix() && (status == "queued" || status == "running") {
			_, _ = p.db.ExecContext(ctx, "UPDATE desktop_chat_jobs SET status='expired' WHERE id=? AND status IN ('queued','running')", jobID)
			return hermes.PromptResult{}, errors.New("Codex Desktop 研究任务已过期")
		}
		switch status {
		case "completed":
			var result struct {
				Content string `json:"content"`
				Thread  string `json:"thread_id"`
				Model   string `json:"model"`
				Effort  string `json:"reasoning_effort"`
			}
			if json.Unmarshal([]byte(raw), &result) != nil || strings.TrimSpace(result.Content) == "" || result.Model != p.model || result.Effort != p.effort {
				return hermes.PromptResult{}, errors.New("Codex Desktop 研究结果与所选模型不一致")
			}
			p.thread = result.Thread
			return hermes.PromptResult{Content: result.Content, SessionID: p.session, StoredSessionID: p.session}, nil
		case "failed":
			var result struct {
				ErrorCode string `json:"error_code"`
			}
			_ = json.Unmarshal([]byte(raw), &result)
			return hermes.PromptResult{}, fmt.Errorf("Codex Desktop 研究失败：%s", firstNonEmpty(result.ErrorCode, "模型调用失败"))
		case "expired", "cancelled":
			return hermes.PromptResult{}, fmt.Errorf("Codex Desktop 研究任务%s", status)
		}
		select {
		case <-ctx.Done():
			_, _ = p.db.Exec("UPDATE desktop_chat_jobs SET status='cancelled' WHERE id=? AND owner=? AND status IN ('queued','running')", jobID, p.owner)
			return hermes.PromptResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *stockResearchCodexPrompter) Close() {
	if p == nil || p.db == nil {
		return
	}
	_, _ = p.db.Exec("UPDATE desktop_chat_jobs SET status='cancelled' WHERE session_id=? AND owner=? AND status IN ('queued','running')", p.session, p.owner)
	_, _ = p.db.Exec("DELETE FROM desktop_chat_jobs WHERE session_id=? AND owner=?", p.session, p.owner)
	_, _ = p.db.Exec("DELETE FROM desktop_chat_sessions WHERE id=? AND owner=?", p.session, p.owner)
	_ = p.db.Close()
	p.db = nil
}
