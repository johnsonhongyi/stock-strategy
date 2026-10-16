package httpapi

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type desktopChatConfig struct {
	Provider  string `json:"provider"`
	Node      string `json:"node_id"`
	Workspace string `json:"workspace_id"`
	Model     string `json:"model"`
	Effort    string `json:"reasoning_effort"`
}
type desktopChatModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts"`
}

func chatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
func (s *Server) chatDB() (*sql.DB, error) {
	db, err := followDB(filepath.Join(filepath.Dir(s.autoFollowDBPath), "control.db"))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS desktop_chat_config(id INTEGER PRIMARY KEY,body TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS desktop_chat_sessions(id TEXT PRIMARY KEY,owner TEXT NOT NULL,node_id TEXT NOT NULL,workspace_id TEXT NOT NULL,model TEXT NOT NULL,effort TEXT NOT NULL,thread_id TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS desktop_chat_jobs(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,owner TEXT NOT NULL,node_id TEXT NOT NULL,workspace_id TEXT NOT NULL,body TEXT NOT NULL,status TEXT NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,lease_token TEXT NOT NULL DEFAULT '',lease_until INTEGER NOT NULL DEFAULT 0,result TEXT NOT NULL DEFAULT '{}');`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func loadDesktopChatConfig(db *sql.DB) desktopChatConfig {
	c := desktopChatConfig{Provider: "hermes", Model: "gpt-6-luna", Effort: "low"}
	var raw string
	if db.QueryRow("SELECT body FROM desktop_chat_config WHERE id=1").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}
func desktopChatCatalog(db *sql.DB, c desktopChatConfig) ([]desktopChatModel, bool) {
	var status, at, detail string
	var data struct {
		Models       []desktopChatModel `json:"models"`
		Capabilities []string           `json:"capabilities"`
	}
	if db.QueryRow("SELECT status,heartbeat,detail FROM ai_desktop WHERE node_id=? AND workspace_id=?", c.Node, c.Workspace).Scan(&status, &at, &detail) != nil {
		return nil, false
	}
	if status != "ready" && status != "analyzing" {
		return nil, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, at)
	age := time.Since(stamp)
	if err != nil || age < 0 || age > 90*time.Second {
		return nil, false
	}
	if json.Unmarshal([]byte(detail), &data) != nil {
		return nil, false
	}
	supportsChat := false
	for _, capability := range data.Capabilities {
		if capability == "ai_chat_v1" {
			supportsChat = true
		}
	}
	return data.Models, supportsChat
}
func validDesktopSelection(c desktopChatConfig, models []desktopChatModel) bool {
	for _, m := range models {
		if m.ID == c.Model {
			for _, e := range m.Efforts {
				if e == c.Effort {
					return true
				}
			}
		}
	}
	return false
}
func (s *Server) desktopChatRuntime(w http.ResponseWriter, r *http.Request) {
	db, err := s.chatDB()
	if err != nil {
		writeError(w, 503, "AI运行时设置不可用")
		return
	}
	defer db.Close()
	c := loadDesktopChatConfig(db)
	if r.Method == http.MethodPut {
		if !s.followCanControl(r) || (r.Header.Get("Origin") != "" && !webSameOrigin(r)) {
			writeError(w, 403, "需要管理员权限")
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&c) != nil || ensureJSONEOF(decoder) != nil || (c.Provider != "hermes" && c.Provider != "codex_desktop") {
			writeError(w, 400, "无效AI运行时设置")
			return
		}
		if c.Provider == "codex_desktop" {
			models, online := desktopChatCatalog(db, c)
			if s.linkage == nil || !s.linkage.IsConfiguredTarget(c.Workspace, c.Node) || !online || !validDesktopSelection(c, models) {
				writeError(w, 400, "桌面离线或模型/推理强度不可用")
				return
			}
		}
		raw, _ := json.Marshal(c)
		if _, err = db.Exec("INSERT OR REPLACE INTO desktop_chat_config VALUES(1,?)", string(raw)); err != nil {
			writeError(w, 503, "保存AI运行时失败")
			return
		}
	}
	// Allow settings to inspect a registered node before saving a selection.
	catalogConfig := c
	if node := r.URL.Query().Get("node_id"); node != "" {
		catalogConfig.Node = node
		catalogConfig.Workspace = r.URL.Query().Get("workspace_id")
	}
	models, online := desktopChatCatalog(db, catalogConfig)
	writeJSON(w, 200, map[string]any{"data": map[string]any{"config": c, "models": models, "online": online, "credential_mode": "desktop_login", "scope": "ai-chat", "message": "Codex登录凭据仅保留在桌面；Hermes技能与MCP配置不自动转交"}})
}
func (s *Server) desktopChatOwner(r *http.Request) string {
	if s.webAuth != nil {
		if u, ok := s.webAuth.user(r); ok {
			return fmt.Sprint(u.ID)
		}
	}
	return "local-desktop"
}
func (s *Server) desktopChatJob(w http.ResponseWriter, r *http.Request) {
	allowed := true
	if s.webAuth != nil {
		_, logged := s.webAuth.user(r)
		allowed = logged || webLAN(r)
	}
	if !allowed || (r.Method != http.MethodGet && r.Header.Get("Origin") != "" && !webSameOrigin(r)) {
		writeError(w, 403, "需要登录并使用同源对话入口")
		return
	}
	db, err := s.chatDB()
	if err != nil {
		writeError(w, 503, "对话队列不可用")
		return
	}
	defer db.Close()
	owner := s.desktopChatOwner(r)
	now := time.Now().Unix()
	if r.Method == http.MethodPost {
		var input struct {
			Prompt     string `json:"prompt"`
			Session    string `json:"session_id"`
			Analysis   string `json:"analysis_id"`
			BridgeTest bool   `json:"bridge_test,omitempty"`
			Node       string `json:"node_id,omitempty"`
			Workspace  string `json:"workspace_id,omitempty"`
			Model      string `json:"model,omitempty"`
			Effort     string `json:"reasoning_effort,omitempty"`
			Messages   []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || ensureJSONEOF(decoder) != nil {
			writeError(w, 400, "无效对话请求")
			return
		}
		if input.BridgeTest {
			if input.Session != "" || input.Analysis != "" || len(input.Messages) != 0 || input.Node == "" || input.Workspace == "" || input.Model == "" || input.Effort == "" {
				writeError(w, 400, "无效桌面桥测试请求")
				return
			}
			input.Prompt = "请只回复 CODEX_BRIDGE_OK，不调用工具，用于桌面桥全链路连通性检测。"
		}
		if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > 16000 || len(input.Messages) > 40 {
			writeError(w, 400, "无效对话请求")
			return
		}
		for _, m := range input.Messages {
			if (m.Role != "user" && m.Role != "assistant") || len(m.Content) > 16000 {
				writeError(w, 400, "无效历史消息")
				return
			}
		}
		c := loadDesktopChatConfig(db)
		if input.BridgeTest {
			c = desktopChatConfig{Provider: "codex_desktop", Node: input.Node, Workspace: input.Workspace, Model: input.Model, Effort: input.Effort}
		}
		models, online := desktopChatCatalog(db, c)
		if c.Provider != "codex_desktop" || !online || !validDesktopSelection(c, models) {
			writeError(w, 503, "Codex桌面对话未就绪")
			return
		}
		if s.linkage == nil || !s.linkage.IsConfiguredTarget(c.Workspace, c.Node) {
			writeError(w, 503, "桌面节点已失效")
			return
		}
		tx, err := db.Begin()
		if err != nil {
			writeError(w, 503, "对话队列忙")
			return
		}
		defer tx.Rollback()
		sid := input.Session
		thread := ""
		if sid != "" {
			var model, effort, node, workspace string
			if tx.QueryRow("SELECT thread_id,model,effort,node_id,workspace_id FROM desktop_chat_sessions WHERE id=? AND owner=?", sid, owner).Scan(&thread, &model, &effort, &node, &workspace) != nil {
				writeError(w, 404, "对话会话不存在")
				return
			}
			if model != c.Model || effort != c.Effort || node != c.Node || workspace != c.Workspace {
				writeError(w, 409, "配置已变化，请新建对话")
				return
			}
		} else {
			sid = chatID()
			if sid == "" {
				writeError(w, 503, "会话创建失败")
				return
			}
			if _, err = tx.Exec("INSERT INTO desktop_chat_sessions(id,owner,node_id,workspace_id,model,effort) VALUES(?,?,?,?,?,?)", sid, owner, c.Node, c.Workspace, c.Model, c.Effort); err != nil {
				writeError(w, 503, "会话创建失败")
				return
			}
		}
		var count int
		_ = tx.QueryRow("SELECT count(*) FROM desktop_chat_jobs WHERE node_id=? AND status IN ('queued','running') AND expires_at>?", c.Node, now).Scan(&count)
		if count >= 8 {
			writeError(w, 429, "桌面对话队列已满")
			return
		}
		_ = tx.QueryRow("SELECT count(*) FROM desktop_chat_jobs WHERE session_id=? AND status IN ('queued','running') AND expires_at>?", sid, now).Scan(&count)
		if count > 0 {
			writeError(w, 409, "当前会话仍有任务运行")
			return
		}
		frame, _ := json.Marshal(map[string]any{"method": "prompt.submit", "params": map[string]any{"text": input.Prompt, "analysis_id": input.Analysis}})
		if !input.BridgeTest {
			frame = s.enrichHermesPrompt(r.Context(), frame)
		}
		var enriched struct {
			Params struct {
				Text string `json:"text"`
			} `json:"params"`
		}
		_ = json.Unmarshal(frame, &enriched)
		body, _ := json.Marshal(map[string]any{"prompt": enriched.Params.Text, "messages": input.Messages, "thread_id": thread, "model": c.Model, "reasoning_effort": c.Effort, "bridge_test": input.BridgeTest})
		id := chatID()
		if id == "" {
			writeError(w, 503, "任务创建失败")
			return
		}
		if _, err = tx.Exec("INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at) VALUES(?,?,?,?,?,?,'queued',?,?)", id, sid, owner, c.Node, c.Workspace, string(body), now, now+600); err != nil {
			writeError(w, 503, "任务创建失败")
			return
		}
		if tx.Commit() != nil {
			writeError(w, 503, "任务保存失败")
			return
		}
		writeJSON(w, 202, map[string]any{"data": map[string]any{"id": id, "session_id": sid, "status": "queued"}})
		return
	}
	id := r.PathValue("id")
	if r.Method == http.MethodDelete {
		_, err = db.Exec("UPDATE desktop_chat_jobs SET status='cancelled' WHERE id=? AND owner=? AND status IN ('queued','running')", id, owner)
		if err != nil {
			writeError(w, 503, "停止失败")
			return
		}
	}
	var status, raw, sid, body string
	var expires, createdAt int64
	if db.QueryRow("SELECT status,result,session_id,expires_at,created_at,body FROM desktop_chat_jobs WHERE id=? AND owner=?", id, owner).Scan(&status, &raw, &sid, &expires, &createdAt, &body) != nil {
		writeError(w, 404, "对话任务不存在")
		return
	}
	// Keep expired running leases reclaimable so the desktop can replay its locally cached result.
	if stale := expires <= now || status == "queued" && createdAt+90 <= now; stale && (status == "queued" || status == "running") {
		updated, updateErr := db.Exec("UPDATE desktop_chat_jobs SET status='expired' WHERE id=? AND owner=? AND status=? AND (expires_at<=? OR (status='queued' AND created_at+90<=?))", id, owner, status, now, now)
		if updateErr == nil {
			if count, _ := updated.RowsAffected(); count == 1 {
				status = "expired"
			} else {
				_ = db.QueryRow("SELECT status FROM desktop_chat_jobs WHERE id=? AND owner=?", id, owner).Scan(&status)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"id": id, "session_id": sid, "status": status, "result": json.RawMessage(raw)}})
	var testJob struct {
		BridgeTest bool `json:"bridge_test"`
	}
	if r.Method == http.MethodDelete && json.Unmarshal([]byte(body), &testJob) == nil && testJob.BridgeTest && status != "queued" && status != "running" {
		_, _ = db.Exec("DELETE FROM desktop_chat_jobs WHERE id=? AND owner=?", id, owner)
		_, _ = db.Exec("DELETE FROM desktop_chat_sessions WHERE id=? AND owner=?", sid, owner)
	}
}
func desktopChatPeer(db *sql.DB, operation, node, workspace, id, token, status string, result json.RawMessage) (map[string]any, error) {
	now := time.Now().Unix()
	if operation == "chat_claim" {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err := tx.Exec("UPDATE desktop_chat_jobs SET status='expired' WHERE node_id=? AND workspace_id=? AND status='queued' AND created_at+90<=?", node, workspace, now); err != nil {
			return nil, err
		}
		var raw string
		err = tx.QueryRow("SELECT id,body FROM desktop_chat_jobs WHERE node_id=? AND workspace_id=? AND expires_at>? AND (status='queued' OR (status='running' AND lease_until<?)) ORDER BY created_at LIMIT 1", node, workspace, now, now).Scan(&id, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"job": nil}, nil
		}
		if err != nil {
			return nil, err
		}
		token = chatID()
		if token == "" {
			return nil, errors.New("lease unavailable")
		}
		updated, err := tx.Exec("UPDATE desktop_chat_jobs SET status='running',lease_token=?,lease_until=? WHERE id=? AND (status='queued' OR (status='running' AND lease_until<?))", token, now+360, id, now)
		if err != nil {
			return nil, err
		}
		n, _ := updated.RowsAffected()
		if n != 1 {
			return nil, errors.New("lease conflict")
		}
		if tx.Commit() != nil {
			return nil, errors.New("lease commit failed")
		}
		return map[string]any{"job": map[string]any{"id": id, "lease_token": token, "context": json.RawMessage(raw)}}, nil
	}
	if status != "running" && status != "completed" && status != "failed" {
		return nil, errors.New("invalid chat status")
	}
	var payload struct {
		Content string `json:"content"`
		Thread  string `json:"thread_id"`
		Model   string `json:"model"`
		Effort  string `json:"reasoning_effort"`
		Error   string `json:"error_code"`
	Stage   string `json:"stage"`
	}
	if json.Unmarshal(result, &payload) != nil || len(payload.Content) > 128<<10 || (status == "completed" && (strings.TrimSpace(payload.Content) == "" || payload.Thread == "")) {
		return nil, errors.New("invalid chat result")
	}
	var body string
	if db.QueryRow("SELECT body FROM desktop_chat_jobs WHERE id=? AND node_id=? AND workspace_id=?", id, node, workspace).Scan(&body) != nil {
		return nil, errors.New("chat not found")
	}
	var frozen struct {
		Model  string `json:"model"`
		Effort string `json:"reasoning_effort"`
	}
	if json.Unmarshal([]byte(body), &frozen) != nil {
		return nil, errors.New("chat config invalid")
	}
	if status != "failed" && (payload.Model != frozen.Model || payload.Effort != frozen.Effort) {
		return nil, errors.New("chat model mismatch")
	}
	if payload.Thread != "" {
		if matched, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, payload.Thread); !matched {
			return nil, errors.New("invalid chat thread")
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	leaseUntil := now
	if status == "running" {
		leaseUntil = now + 360
		if payload.Stage == "result_writeback" {
			leaseUntil = now + 15
		}
	}
	updated, err := tx.Exec("UPDATE desktop_chat_jobs SET status=?,result=?,lease_until=? WHERE id=? AND node_id=? AND workspace_id=? AND status='running' AND lease_token=? AND lease_until>? AND expires_at>?", status, string(result), leaseUntil, id, node, workspace, token, now, now)
	if err != nil {
		return nil, err
	}
	n, _ := updated.RowsAffected()
	if n != 1 {
		var storedStatus, storedToken, storedResult string
		if queryErr := tx.QueryRow("SELECT status,lease_token,result FROM desktop_chat_jobs WHERE id=? AND node_id=? AND workspace_id=?", id, node, workspace).Scan(&storedStatus, &storedToken, &storedResult); queryErr == nil &&
			storedToken == token && storedStatus == status && (status == "completed" || status == "failed") && sameDesktopChatResult(storedResult, result) {
			return map[string]any{"status": "stored", "duplicate": true}, nil
		}
		return nil, errors.New("chat lease inactive or cancelled")
	}
	if payload.Thread != "" {
		_, err = tx.Exec("UPDATE desktop_chat_sessions SET thread_id=? WHERE id=(SELECT session_id FROM desktop_chat_jobs WHERE id=?)", payload.Thread, id)
		if err != nil {
			return nil, err
		}
	}
	if tx.Commit() != nil {
		return nil, errors.New("chat result commit failed")
	}
	return map[string]any{"status": "stored"}, nil
}

func sameDesktopChatResult(stored string, incoming []byte) bool {
	var left, right any
	if json.Unmarshal([]byte(stored), &left) != nil || json.Unmarshal(incoming, &right) != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
