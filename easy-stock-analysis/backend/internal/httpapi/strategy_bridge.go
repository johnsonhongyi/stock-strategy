package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

func desktopStrategyCatalog(db *sql.DB, config desktopChatConfig) ([]desktopChatModel, bool) {
	return desktopCatalogForCapability(db, config, "strategy_research")
}

// Reconcile only future AI routing through the worker's existing command queue.
// Account/risk settings and the immutable routing of queued/running jobs stay owned by the worker.
func (s *Server) reconcileStrategyBridge(ctx context.Context, control *sql.DB) error {
	if s.linkage == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	account, err := followDB(s.autoFollowDBPath)
	if err != nil {
		return err
	}
	var raw string
	err = account.QueryRowContext(ctx, "SELECT body FROM state WHERE id=1").Scan(&raw)
	account.Close()
	if err != nil {
		return err
	}
	var state map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return err
	}
	var settings map[string]any
	if len(state["ai_settings"]) == 0 {
		return nil
	}
	if err = json.Unmarshal(state["ai_settings"], &settings); err != nil {
		return err
	}
	text := func(key string) string { value, _ := settings[key].(string); return value }
	if text("mode") != "shadow" && text("mode") != "enforce" {
		return nil
	}
	if enabled, _ := settings["enabled"].(bool); !enabled && text("node_id") == "" {
		return nil
	}
	current := desktopChatConfig{Node: text("node_id"), Workspace: text("workspace_id"), Model: text("model"), Effort: text("reasoning_effort")}
	if models, online := desktopStrategyCatalog(control, current); online && validDesktopSelection(current, models) {
		return nil
	}
	next := current
	found := false
	targets := s.linkage.Targets()
	for _, sameWorkspace := range []bool{true, false} {
		for _, target := range targets {
			if (target["workspace_id"] == current.Workspace) != sameWorkspace || !s.linkage.IsConfiguredTarget(target["workspace_id"], target["node_id"]) {
				continue
			}
			candidate := current
			candidate.Node, candidate.Workspace = target["node_id"], target["workspace_id"]
			models, online := desktopStrategyCatalog(control, candidate)
			if !online {
				continue
			}
			usable := []desktopChatModel{}
			for _, model := range models {
				if model.ID != "" && len(model.Efforts) > 0 {
					usable = append(usable, model)
				}
			}
			if len(usable) == 0 {
				continue
			}
			selected := usable[0]
			for _, model := range usable {
				if model.ID == current.Model {
					selected = model
					break
				}
			}
			candidate.Model = selected.ID
			candidate.Effort = selected.Efforts[0]
			for _, effort := range selected.Efforts {
				if effort == current.Effort {
					candidate.Effort = effort
					break
				}
			}
			next, found = candidate, true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return nil
	}
	previous, _ := json.Marshal(settings)
	settings["node_id"], settings["workspace_id"], settings["model"], settings["reasoning_effort"] = next.Node, next.Workspace, next.Model, next.Effort
	// Keep the worker's strict command contract; cadence stays in worker state.
	payload := map[string]any{}
	for _, key := range []string{"enabled", "mode", "node_id", "workspace_id", "model", "reasoning_effort"} {
		payload[key] = settings[key]
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var oldHeartbeat string
	_ = control.QueryRowContext(ctx, "SELECT heartbeat FROM ai_desktop WHERE node_id=? AND workspace_id=?", current.Node, current.Workspace).Scan(&oldHeartbeat)
	hash := sha256.Sum256(append(append(previous, body...), []byte(oldHeartbeat)...))
	id := "auto-bridge-" + hex.EncodeToString(hash[:12])
	tx, err := control.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM commands WHERE action='ai_settings' AND status='pending'").Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO commands(id,action,payload,created_at) VALUES(?,'ai_settings',?,?) ON CONFLICT(id) DO NOTHING", id, string(body), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func overlayStrategyBridgeState(ctx context.Context, control *sql.DB, state map[string]any) {
	settings, _ := state["ai_settings"].(map[string]any)
	if settings == nil {
		return
	}
	node, _ := settings["node_id"].(string)
	workspace, _ := settings["workspace_id"].(string)
	models, online := desktopStrategyCatalog(control, desktopChatConfig{Node: node, Workspace: workspace})
	ai, _ := state["ai"].(map[string]any)
	if ai == nil {
		ai = map[string]any{}
	}
	var status, heartbeat, raw string
	_ = control.QueryRowContext(ctx, "SELECT status,heartbeat,detail FROM ai_desktop WHERE node_id=? AND workspace_id=?", node, workspace).Scan(&status, &heartbeat, &raw)
	ai["desktop_online"], ai["desktop_heartbeat"] = online, heartbeat
	if online {
		var detail map[string]any
		_ = json.Unmarshal([]byte(raw), &detail)
		if detail == nil {
			detail = map[string]any{}
		}
		detail["models"] = models
		ai["desktop_status"], ai["desktop_detail"] = status, detail
	} else {
		ai["desktop_status"] = "offline"
		ai["desktop_detail"] = map[string]any{"error_code": "BRIDGE_OFFLINE"}
	}
	state["ai"] = ai
}

func automaticBridgeNotice(id, action, status string) string {
	if action != "ai_settings" || !strings.HasPrefix(id, "auto-bridge-") {
		return ""
	}
	if status == "pending" {
		return "原策略桥已离线，正在自动对齐到有心跳的可用桌面桥。"
	}
	if status == "applied" {
		return "策略桥已自动对齐到有心跳的可用节点；模型和推理强度按可用目录保留。"
	}
	return "策略桥自动对齐尚未完成，请查看指令回执。"
}
