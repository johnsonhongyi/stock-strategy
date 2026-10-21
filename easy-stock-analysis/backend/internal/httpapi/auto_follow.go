package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func followDB(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(3000)")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func (s *Server) followCanControl(r *http.Request) bool {
	if s.webAuth == nil {
		return true
	}
	user, logged := s.webAuth.user(r)
	if logged {
		return user.Admin
	}
	return webLAN(r)
}

func (s *Server) autoFollowStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result := map[string]any{"available": false, "online": false, "can_control": s.followCanControl(r), "commands": []any{}, "state": nil}
	db, err := followDB(s.autoFollowDBPath)
	if os.IsNotExist(err) {
		writeJSON(w, 200, map[string]any{"data": result})
		return
	}
	if err != nil {
		writeError(w, 503, "模拟账户暂不可读")
		return
	}
	defer db.Close()
	var body string
	if err = db.QueryRowContext(r.Context(), "SELECT body FROM state WHERE id=1").Scan(&body); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, 200, map[string]any{"data": result})
			return
		}
		writeError(w, 503, "模拟账户读取失败")
		return
	}
	var state map[string]any
	if json.Unmarshal([]byte(body), &state) != nil || state["mode"] != "paper" {
		writeError(w, 503, "模拟账户格式异常")
		return
	}
	heartbeat, _ := state["last_heartbeat"].(string)
	last, _ := time.Parse(time.RFC3339, heartbeat)
	age := time.Since(last).Seconds()
	result["available"] = true
	result["online"] = !last.IsZero() && age >= 0 && age < 240
	if !last.IsZero() {
		result["heartbeat_age_seconds"] = int(age)
	}
	delete(state, "command_receipts")
	// Keep account totals, bound the browser's history payload.
	closed, _ := state["closed"].([]any)
	realized, wins := 0.0, 0
	for _, v := range closed {
		if p, ok := v.(map[string]any); ok {
			if n, ok := p["pnl"].(float64); ok {
				realized += n
				if n > 0 {
					wins++
				}
			}
		}
	}
	result["summary"] = map[string]any{"realized_pnl": realized, "closed_count": len(closed), "winning_count": wins}
	if len(closed) > 200 {
		state["closed"] = closed[len(closed)-200:]
	}
	if events, ok := state["events"].([]any); ok && len(events) > 300 {
		state["events"] = events[len(events)-300:]
	}
	result["state"] = state
	control, err := followDB(filepath.Join(filepath.Dir(s.autoFollowDBPath), "control.db"))
	if err == nil {
		defer control.Close()
		var runtimeHeartbeat, phase string
		if control.QueryRowContext(r.Context(), "SELECT heartbeat,phase FROM runtime WHERE id=1").Scan(&runtimeHeartbeat, &phase) == nil {
			if t, e := time.Parse(time.RFC3339, runtimeHeartbeat); e == nil {
				age := time.Since(t).Seconds()
				result["online"] = age >= 0 && age < 240
				result["heartbeat_age_seconds"] = int(age)
				state["last_heartbeat"] = runtimeHeartbeat
				state["runtime_phase"] = phase
			}
		}

		rows, e := control.QueryContext(r.Context(), "SELECT id,action,created_at,status,applied_at,result FROM commands ORDER BY created_at DESC,id DESC LIMIT 30")
		if e == nil {
			defer rows.Close()
			commands := []map[string]any{}
			settingsSeen := false
			for rows.Next() {
				var id, action, created, status string
				var applied, message sql.NullString
				if rows.Scan(&id, &action, &created, &status, &applied, &message) != nil {
					break
				}
				if action == "ai_settings" && !settingsSeen {
					result["bridge_notice"] = automaticBridgeNotice(id, action, status)
					settingsSeen = true
				}
				commands = append(commands, map[string]any{"id": id, "action": action, "created_at": created, "status": status, "applied_at": applied.String, "result": message.String})
			}
			result["commands"] = commands
			rows.Close()
		}
		overlayStrategyBridgeState(r.Context(), control, state)
	}
	writeJSON(w, 200, map[string]any{"data": result})
}

func (s *Server) autoFollowCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.followCanControl(r) {
		writeError(w, 403, "需要管理员权限")
		return
	}
	if r.Header.Get("Origin") != "" && !webSameOrigin(r) {
		writeError(w, 403, "invalid origin")
		return
	}
	var input struct {
		ID         string          `json:"id"`
		Action     string          `json:"action"`
		Settings   json.RawMessage `json:"settings"`
		AISettings json.RawMessage `json:"ai_settings"`
		Question   string          `json:"question"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "无效的跟单指令")
		return
	}
	if len(input.ID) < 8 || len(input.ID) > 80 || strings.ContainsAny(input.ID, "\r\n\x00") {
		writeError(w, 400, "指令ID无效")
		return
	}
	payload := "{}"
	switch input.Action {
	case "pause", "resume", "kill", "release_kill", "scan":
	case "ai_ask":
		if len([]rune(strings.TrimSpace(input.Question))) < 1 || len([]rune(input.Question)) > 4000 {
			writeError(w, 400, "invalid AI question")
			return
		}
		b, _ := json.Marshal(map[string]string{"question": strings.TrimSpace(input.Question)})
		payload = string(b)
	case "ai_settings":
		var settings struct {
			Enabled         *bool  `json:"enabled"`
			Mode            string `json:"mode"`
			NodeID          string `json:"node_id"`
			WorkspaceID     string `json:"workspace_id"`
			Model           string `json:"model,omitempty"`
			ReasoningEffort string `json:"reasoning_effort,omitempty"`
		}
		d := json.NewDecoder(strings.NewReader(string(input.AISettings)))
		d.DisallowUnknownFields()
		if d.Decode(&settings) != nil || ensureJSONEOF(d) != nil || settings.Enabled == nil || (settings.Mode != "shadow" && settings.Mode != "enforce") || s.linkage == nil || !s.linkage.IsConfiguredTarget(settings.WorkspaceID, settings.NodeID) {
			writeError(w, 400, "invalid AI desktop settings")
			return
		}
		validEffort := settings.ReasoningEffort == ""
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
			if settings.ReasoningEffort == effort {
				validEffort = true
			}
		}
		if len(settings.Model) > 96 || strings.ContainsAny(settings.Model, " /\\\r\n") || !validEffort {
			writeError(w, 400, "invalid model or reasoning effort")
			return
		}
		b, _ := json.Marshal(settings)
		payload = string(b)
	case "settings":
		var settings struct {
			LimitUp       *bool    `json:"limit_up"`
			ThemeTrend    *bool    `json:"theme_trend"`
			ThemeIDs      []string `json:"theme_ids"`
			ManualSymbols []string `json:"manual_symbols,omitempty"`
		}
		d := json.NewDecoder(strings.NewReader(string(input.Settings)))
		d.DisallowUnknownFields()
		if d.Decode(&settings) != nil || settings.LimitUp == nil || settings.ThemeTrend == nil || len(settings.ThemeIDs) > 10 || len(settings.ManualSymbols) > 30 {
			writeError(w, 400, "跟随配置无效")
			return
		}
		for _, id := range settings.ThemeIDs {
			if len(id) == 0 || len(id) > 80 || strings.ContainsAny(id, "\r\n\x00") {
				writeError(w, 400, "题材ID无效")
				return
			}
		}
		for _, sym := range settings.ManualSymbols {
			if len(sym) == 0 || len(sym) > 20 || strings.ContainsAny(sym, "\r\n\x00") {
				writeError(w, 400, "自定义股票代码无效")
				return
			}
		}
		if settings.ThemeIDs == nil {
			settings.ThemeIDs = []string{}
		}
		if settings.ManualSymbols == nil {
			settings.ManualSymbols = []string{}
		}
		b, _ := json.Marshal(settings)
		payload = string(b)
	default:
		writeError(w, 400, "仅允许模拟跟单控制指令")
		return
	}
	db, err := followDB(filepath.Join(filepath.Dir(s.autoFollowDBPath), "control.db"))
	if err != nil {
		writeError(w, 503, "跟单控制队列未就绪")
		return
	}
	defer db.Close()
	_, err = db.ExecContext(r.Context(), "INSERT INTO commands(id,action,payload,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", input.ID, input.Action, payload, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeError(w, 503, "控制队列繁忙，请稍后重试")
		return
	}
	var action, stored, status string
	if db.QueryRowContext(r.Context(), "SELECT action,payload,status FROM commands WHERE id=?", input.ID).Scan(&action, &stored, &status) != nil {
		writeError(w, 503, "指令回执暂不可读")
		return
	}
	if action != input.Action || stored != payload {
		writeError(w, 409, "指令ID已用于其它操作")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]string{"id": input.ID, "status": status, "message": fmt.Sprintf("%s 已入队，等待 worker 回执", action)}})
}
