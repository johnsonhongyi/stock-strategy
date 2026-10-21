package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const automaticResearchSchema = `
CREATE TABLE IF NOT EXISTS ai_jobs(id TEXT PRIMARY KEY,trade_date TEXT NOT NULL,evidence_hash TEXT NOT NULL,node_id TEXT NOT NULL,workspace_id TEXT NOT NULL,context TEXT NOT NULL,question TEXT NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'queued',lease_token TEXT,lease_until TEXT,result TEXT,result_hash TEXT,finished_at TEXT);
CREATE TABLE IF NOT EXISTS automatic_research_policy(id INTEGER PRIMARY KEY,body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS automatic_research_runs(job_id TEXT PRIMARY KEY,day TEXT NOT NULL,started_at TEXT NOT NULL,token_budget INTEGER NOT NULL,total_tokens INTEGER,usage_known INTEGER NOT NULL DEFAULT 0);`

type automaticResearchPolicy struct {
	Enabled     bool  `json:"enabled"`
	Interval    int   `json:"interval_seconds"`
	MaxRuns     int   `json:"daily_max_runs"`
	TokenBudget int64 `json:"daily_token_budget"`
	Revision    int64 `json:"revision"`
}

func loadAutomaticResearchPolicy(db *sql.DB) (automaticResearchPolicy, error) {
	p := automaticResearchPolicy{Interval: 900, MaxRuns: 3, TokenBudget: 300000}
	var raw string
	err := db.QueryRow("SELECT body FROM automatic_research_policy WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p)
	if err == nil && (p.Interval < 300 || p.Interval > 86400 || p.MaxRuns < 1 || p.MaxRuns > 100 || p.TokenBudget < 1000 || p.TokenBudget > 10000000) {
		err = errors.New("invalid automatic research policy")
	}
	return p, err
}

func automaticResearchDay() string {
	return time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
}

type automaticResearchUsage struct {
	Runs    int   `json:"runs"`
	Tokens  int64 `json:"total_tokens"`
	Unknown int   `json:"unsettled_runs"`
}

type dbExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func autoReconcileResearchRuns(e dbExecer) error {
	now := time.Now()
	nowUnix := now.Unix()
	nowISO := now.UTC().Format(time.RFC3339Nano)
	_, _ = e.Exec(`
		UPDATE ai_jobs 
		SET status='expired' 
		WHERE status IN ('queued', 'running') 
		  AND (
		    expires_at <= ? 
		    OR (status='running' AND CAST(COALESCE(lease_until, '0') AS INTEGER) < ? AND CAST(COALESCE(lease_until, '0') AS INTEGER) > 0)
		  )
	`, nowISO, nowUnix)
	_, err := e.Exec(`
		UPDATE automatic_research_runs 
		SET usage_known=1, total_tokens=COALESCE(total_tokens, 0) 
		WHERE usage_known=0 
		  AND job_id IN (SELECT id FROM ai_jobs WHERE status IN ('expired', 'failed', 'cancelled', 'completed'))
	`)
	_, _ = e.Exec(`
		DELETE FROM ai_jobs 
		WHERE status IN ('expired', 'cancelled') 
		  AND id NOT IN (
		    SELECT id FROM ai_jobs 
		    WHERE status IN ('expired', 'cancelled') 
		    ORDER BY created_at DESC 
		    LIMIT 3
		  )
	`)
	cutoff7d := now.AddDate(0, 0, -7).UTC().Format(time.RFC3339Nano)
	_, _ = e.Exec("DELETE FROM ai_jobs WHERE status='completed' AND created_at < ?", cutoff7d)
	cutoff30d := now.AddDate(0, 0, -30).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
	_, _ = e.Exec("DELETE FROM automatic_research_runs WHERE day < ?", cutoff30d)
	return err
}

func automaticResearchDaily(db *sql.DB) (automaticResearchUsage, error) {
	_ = autoReconcileResearchRuns(db)
	var u automaticResearchUsage
	err := db.QueryRow("SELECT count(*),COALESCE(sum(total_tokens),0),COALESCE(sum(CASE WHEN usage_known=0 THEN 1 ELSE 0 END),0) FROM automatic_research_runs WHERE day=?", automaticResearchDay()).Scan(&u.Runs, &u.Tokens, &u.Unknown)
	return u, err
}

func (s *Server) automaticResearchState(db *sql.DB, p automaticResearchPolicy) (desktopChatConfig, automaticResearchUsage, string, error) {
	c := loadDesktopChatConfig(db)
	u, err := automaticResearchDaily(db)
	if err != nil {
		return c, u, "policy_unavailable", err
	}
	if !p.Enabled {
		return c, u, "disabled", nil
	}
	if c.Provider != "codex_desktop" && c.Provider != "antigravity_desktop" {
		return c, u, "runtime_unavailable", nil
	}
	models, online := desktopStrategyCatalog(db, c)
	if s.linkage == nil || !s.linkage.IsConfiguredTarget(c.Workspace, c.Node) || !online || !validDesktopSelection(c, models) {
		return c, u, "model_unavailable", nil
	}
	if u.Unknown > 0 {
		return c, u, "usage_unsettled", nil
	}
	if u.Runs >= p.MaxRuns || u.Tokens >= p.TokenBudget {
		return c, u, "budget_exhausted", nil
	}
	var last string
	if db.QueryRow("SELECT started_at FROM automatic_research_runs ORDER BY started_at DESC LIMIT 1").Scan(&last) == nil {
		at, e := time.Parse(time.RFC3339Nano, last)
		if e != nil {
			return c, u, "policy_unavailable", e
		}
		if time.Since(at) < time.Duration(p.Interval)*time.Second {
			return c, u, "interval_wait", nil
		}
	}
	return c, u, "ready", nil
}

func (s *Server) automaticResearch(w http.ResponseWriter, r *http.Request) {
	if !s.followCanControl(r) || (r.Method != http.MethodGet && r.Header.Get("Origin") != "" && !webSameOrigin(r)) {
		writeError(w, 403, "需要管理员及同源访问")
		return
	}
	db, err := s.desktopDB()
	if err != nil {
		writeError(w, 503, "自动投研设置不可用")
		return
	}
	p, err := loadAutomaticResearchPolicy(db)
	if err != nil {
		writeError(w, 503, "自动投研设置不可用")
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			Enabled     bool  `json:"enabled"`
			Interval    int   `json:"interval_seconds"`
			MaxRuns     int   `json:"daily_max_runs"`
			TokenBudget int64 `json:"daily_token_budget"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || ensureJSONEOF(decoder) != nil || input.Interval < 300 || input.Interval > 86400 || input.MaxRuns < 1 || input.MaxRuns > 100 || input.TokenBudget < 1000 || input.TokenBudget > 10000000 {
			writeError(w, 400, "频率需为300–86400秒，每日1–100次，Token预算1000–10000000")
			return
		}
		// A setting change invalidates queued snapshots. Running jobs retain their configuration.
		tx, e := db.BeginTx(r.Context(), nil)
		if e != nil {
			writeError(w, 503, "保存失败")
			return
		}
		defer tx.Rollback()
		defaultRaw, _ := json.Marshal(automaticResearchPolicy{Interval: 900, MaxRuns: 3, TokenBudget: 300000})
		if _, e = tx.Exec("INSERT OR IGNORE INTO automatic_research_policy VALUES(1,?)", string(defaultRaw)); e != nil {
			writeError(w, 503, "保存失败")
			return
		}
		if _, e = tx.Exec("UPDATE automatic_research_policy SET body=body WHERE id=1"); e != nil {
			writeError(w, 503, "保存失败")
			return
		}
		var previous string
		if e = tx.QueryRow("SELECT body FROM automatic_research_policy WHERE id=1").Scan(&previous); e != nil || json.Unmarshal([]byte(previous), &p) != nil {
			writeError(w, 503, "保存失败")
			return
		}
		p = automaticResearchPolicy{input.Enabled, input.Interval, input.MaxRuns, input.TokenBudget, p.Revision + 1}
		raw, _ := json.Marshal(p)
		if _, e = tx.Exec("INSERT OR REPLACE INTO automatic_research_policy VALUES(1,?)", string(raw)); e == nil {
			_, e = tx.Exec("UPDATE ai_jobs SET status='cancelled' WHERE status='queued'")
		}
		if e != nil || tx.Commit() != nil {
			writeError(w, 503, "保存失败")
			return
		}
	}
	if r.Method == http.MethodPost {
		s.settleAutomaticResearchManual(w, r)
		return
	}
	s.renderAutomaticResearch(w, r, db, p)
}

func (s *Server) renderAutomaticResearch(w http.ResponseWriter, r *http.Request, db *sql.DB, p automaticResearchPolicy) {
	c, u, status, err := s.automaticResearchState(db, p)
	if err != nil {
		writeError(w, 503, "用量暂不可读，自动任务暂停")
		return
	}
	const pageSize = 20
	page := 1
	if rawPage := r.URL.Query().Get("page"); rawPage != "" {
		parsedPage, parseErr := strconv.Atoi(rawPage)
		if parseErr != nil || parsedPage < 1 {
			writeError(w, http.StatusBadRequest, "无效的运行记录页码")
			return
		}
		page = parsedPage
	}
	var recordTotal int
	if err := db.QueryRow("SELECT count(*) FROM ai_jobs").Scan(&recordTotal); err != nil {
		writeError(w, 503, "运行记录不可读")
		return
	}
	pageCount := (recordTotal + pageSize - 1) / pageSize
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	offset := (page - 1) * pageSize
	rows, err := db.Query(`SELECT j.id,j.created_at,j.status,j.context,j.result,r.started_at,r.total_tokens,r.usage_known FROM ai_jobs j LEFT JOIN automatic_research_runs r ON j.id=r.job_id ORDER BY j.created_at DESC,j.id DESC LIMIT ? OFFSET ?`, pageSize, offset)
	if err != nil {
		writeError(w, 503, "运行记录不可用")
		return
	}
	defer rows.Close()
	records := []map[string]any{}
	for rows.Next() {
		var id, at, state, body string
		var result, started sql.NullString
		var tokens, known sql.NullInt64
		if rows.Scan(&id, &at, &state, &body, &result, &started, &tokens, &known) != nil {
			writeError(w, 503, "运行记录不可用")
			return
		}
		var ctx struct {
			Runtime desktopChatConfig `json:"ai_runtime"`
			Stage   string            `json:"workflow_stage"`
		}
		_ = json.Unmarshal([]byte(body), &ctx)
		var outcome struct {
			Error string `json:"error_code"`
		}
		_ = json.Unmarshal([]byte(result.String), &outcome)
		records = append(records, map[string]any{"id": id, "created_at": at, "status": state, "model": ctx.Runtime.Model, "reasoning_effort": ctx.Runtime.Effort, "stage": ctx.Stage, "started_at": started.String, "total_tokens": tokens.Int64, "usage_known": known.Int64 == 1, "error_code": outcome.Error})
	}
	if rows.Err() != nil {
		writeError(w, 503, "运行记录不可用")
		return
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"policy": p, "runtime": c, "status": status, "usage": u, "records": records, "records_total": recordTotal, "records_page": page, "records_page_size": pageSize}})
}

func (s *Server) settleAutomaticResearchManual(w http.ResponseWriter, r *http.Request) {
	if !s.followCanControl(r) || (r.Header.Get("Origin") != "" && !webSameOrigin(r)) {
		writeError(w, 403, "需要管理员及同源访问")
		return
	}
	db, err := s.desktopDB()
	if err != nil {
		writeError(w, 503, "自动投研数据库不可用")
		return
	}
	p, err := loadAutomaticResearchPolicy(db)
	if err != nil {
		writeError(w, 503, "自动投研设置不可用")
		return
	}
	now := time.Now()
	nowUnix := now.Unix()
	nowISO := now.UTC().Format(time.RFC3339Nano)
	_, _ = db.Exec(`
		UPDATE ai_jobs 
		SET status='expired' 
		WHERE status IN ('queued', 'running') 
		  AND (
		    expires_at <= ? 
		    OR (status='running' AND CAST(COALESCE(lease_until, '0') AS INTEGER) < ? AND CAST(COALESCE(lease_until, '0') AS INTEGER) > 0)
		  )
	`, nowISO, nowUnix)
	if _, err = db.Exec("UPDATE automatic_research_runs SET usage_known=1, total_tokens=COALESCE(total_tokens, 0) WHERE usage_known=0"); err != nil {
		writeError(w, 503, "结算未确认用量失败")
		return
	}
	_, _ = db.Exec("DELETE FROM ai_jobs WHERE status IN ('expired', 'cancelled')")
	s.renderAutomaticResearch(w, r, db, p)
}

// Admission is serialized by a SQLite write lock across API processes. Old or
// expired attempts are never silently retried; their unknown usage blocks more calls.
func (s *Server) claimAutomaticResearch(ctx context.Context, db *sql.DB, node, workspace string) (map[string]any, error) {
	empty := map[string]any{"job": nil}
	p, err := loadAutomaticResearchPolicy(db)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return empty, nil
	}
	if s.linkage == nil || !s.linkage.IsConfiguredTarget(workspace, node) {
		return empty, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE automatic_research_policy SET body=body WHERE id=1"); err != nil {
		return nil, err
	}
	// All reads inside the transaction avoid lock recursion on the single DB connection.
	var raw string
	if err = tx.QueryRow("SELECT body FROM automatic_research_policy WHERE id=1").Scan(&raw); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	if !p.Enabled {
		return empty, nil
	}
	var configRaw string
	if err = tx.QueryRow("SELECT body FROM desktop_chat_config WHERE id=1").Scan(&configRaw); err != nil {
		return empty, nil
	}
	var c desktopChatConfig
	if json.Unmarshal([]byte(configRaw), &c) != nil || c.Node != node || c.Workspace != workspace || (c.Provider != "codex_desktop" && c.Provider != "antigravity_desktop") {
		return empty, nil
	}
	var heartbeat, detail, status string
	if tx.QueryRow("SELECT status,heartbeat,detail FROM ai_desktop WHERE node_id=? AND workspace_id=?", node, workspace).Scan(&status, &heartbeat, &detail) != nil {
		return empty, nil
	}
	var catalog struct {
		Models       []desktopChatModel `json:"models"`
		Capabilities []string           `json:"capabilities"`
	}
	stamp, e := time.Parse(time.RFC3339Nano, heartbeat)
	capable := false
	_ = json.Unmarshal([]byte(detail), &catalog)
	for _, v := range catalog.Capabilities {
		if v == "strategy_research" {
			capable = true
		}
	}
	if e != nil || time.Since(stamp) < 0 || time.Since(stamp) > 90*time.Second || (status != "ready" && status != "analyzing") || !capable || !validDesktopSelection(c, catalog.Models) {
		return empty, nil
	}
	_ = autoReconcileResearchRuns(tx)
	var used int64
	var runs, unknown int
	if err = tx.QueryRow("SELECT count(*),COALESCE(sum(total_tokens),0),COALESCE(sum(CASE WHEN usage_known=0 THEN 1 ELSE 0 END),0) FROM automatic_research_runs WHERE day=?", automaticResearchDay()).Scan(&runs, &used, &unknown); err != nil {
		return nil, err
	}
	if unknown > 0 || runs >= p.MaxRuns || used >= p.TokenBudget {
		return empty, nil
	}
	var last string
	if tx.QueryRow("SELECT started_at FROM automatic_research_runs ORDER BY started_at DESC LIMIT 1").Scan(&last) == nil {
		at, e := time.Parse(time.RFC3339Nano, last)
		if e != nil || time.Since(at) < time.Duration(p.Interval)*time.Second {
			return empty, nil
		}
	}
	var id, created, expires, hash, body, question string
	err = tx.QueryRow("SELECT id,created_at,expires_at,evidence_hash,context,question FROM ai_jobs WHERE node_id=? AND workspace_id=? AND status='queued' ORDER BY created_at LIMIT 1", node, workspace).Scan(&id, &created, &expires, &hash, &body, &question)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot struct {
		Runtime  desktopChatConfig `json:"ai_runtime"`
		Revision int64             `json:"auto_research_revision"`
	}
	expiry, e := time.Parse(time.RFC3339Nano, expires)
	if json.Unmarshal([]byte(body), &snapshot) != nil || snapshot.Revision != p.Revision || snapshot.Runtime.Model != c.Model || snapshot.Runtime.Effort != c.Effort || e != nil || !expiry.After(time.Now()) {
		_, err = tx.Exec("UPDATE ai_jobs SET status='cancelled' WHERE id=? AND status='queued'", id)
		if err == nil {
			err = tx.Commit()
		}
		return empty, err
	}
	budget := p.TokenBudget - used
	token := chatID() + chatID()
	if len(token) != 64 {
		return nil, errors.New("lease unavailable")
	}
	if _, err = tx.Exec("INSERT INTO automatic_research_runs(job_id,day,started_at,token_budget) VALUES(?,?,?,?)", id, automaticResearchDay(), time.Now().UTC().Format(time.RFC3339Nano), budget); err != nil {
		return nil, err
	}
	if _, err = tx.Exec("UPDATE ai_jobs SET status='running',lease_token=?,lease_until=? WHERE id=? AND status='queued'", token, time.Now().Add(360*time.Second).Unix(), id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"job": map[string]any{"id": id, "created_at": created, "expires_at": expires, "evidence_hash": hash, "context": json.RawMessage(body), "question": question, "lease_token": token, "token_budget": budget}}, nil
}

func settleAutomaticResearch(db *sql.DB, id string, result json.RawMessage) error {
	var envelope struct {
		Usage map[string]int64 `json:"token_usage"`
	}
	total := int64(0)
	known := false
	if json.Unmarshal(result, &envelope) == nil && envelope.Usage != nil {
		total, known = envelope.Usage["totalTokens"]
		if !known {
			total, known = envelope.Usage["total"]
		}
	}
	if !known || total < 0 {
		total = 0
	}
	_, err := db.Exec("UPDATE automatic_research_runs SET total_tokens=?,usage_known=1 WHERE job_id=?", total, id)
	return err
}
