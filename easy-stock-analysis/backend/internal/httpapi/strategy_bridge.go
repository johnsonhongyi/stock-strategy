package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

func desktopStrategyCatalog(db *sql.DB, config desktopChatConfig) ([]desktopChatModel, bool) {
	return desktopCatalogForCapability(db, config, "strategy_research")
}

// Automatic research inherits only the explicitly saved system runtime.
// Never enqueue an implicit routing/settings change when a bridge is unavailable.
func (s *Server) reconcileStrategyBridge(ctx context.Context, control *sql.DB) error {
	return nil
}

func overlayStrategyBridgeState(ctx context.Context, control *sql.DB, state map[string]any) {
	settings, _ := state["ai_settings"].(map[string]any)
	if settings == nil {
		return
	}
	runtime := loadDesktopChatConfig(control)
	node, workspace := runtime.Node, runtime.Workspace
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
