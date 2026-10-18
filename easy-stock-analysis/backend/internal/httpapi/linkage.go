package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"easy-stock/backend/internal/appsettings"
	"easy-stock/backend/internal/linkage"
)

func (s *Server) linkageTargets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	targets := s.linkage.Targets()
	response := map[string]any{
		"schema_version":  1,
		"targets":         targets,
		"default_node_id": s.settingsStore.Snapshot().LinkageDefaultNodeID,
	}
	response["chat_target"] = map[string]string{"provider": "unknown"}
	for _, target := range targets {
		switch {
		case strings.HasPrefix(strings.ToLower(target["node_id"]), "agv-"):
			target["provider"] = "antigravity_desktop"
		case strings.HasPrefix(strings.ToLower(target["node_id"]), "codex-"):
			target["provider"] = "codex_desktop"
		default:
			target["provider"] = "unknown"
		}
		target["online"] = "false"
		target["desktop_status"] = "status_unavailable"
		target["chat_queued"] = "0"
		target["chat_running"] = "0"
		target["chat_jobs_status"] = "status_unavailable"
	}
	if db, err := s.desktopDB(); err == nil {
		chat := loadDesktopChatConfig(db)
		response["chat_target"] = map[string]string{
			"provider": chat.Provider, "node_id": chat.Node, "workspace_id": chat.Workspace,
		}
		if stateDB, stateErr := followDB(s.autoFollowDBPath); stateErr == nil {
			defer stateDB.Close()
			var body string
			if stateDB.QueryRowContext(r.Context(), "SELECT body FROM state WHERE id=1").Scan(&body) == nil {
				var state map[string]any
				if json.Unmarshal([]byte(body), &state) == nil {
					if settings, ok := state["ai_settings"].(map[string]any); ok {
						nodeID, _ := settings["node_id"].(string)
						workspaceID, _ := settings["workspace_id"].(string)
						if nodeID != "" && workspaceID != "" {
							response["strategy_target"] = map[string]string{"node_id": nodeID, "workspace_id": workspaceID}
						}
					}
				}
			}
		}
		for _, target := range targets {
			target["online"] = "false"
			target["desktop_status"] = "no_heartbeat"
			var status, heartbeat string
			err := db.QueryRowContext(r.Context(), "SELECT status,heartbeat FROM ai_desktop WHERE node_id=? AND workspace_id=?", target["node_id"], target["workspace_id"]).Scan(&status, &heartbeat)
			if err == nil {
				target["desktop_status"] = status
				target["heartbeat"] = heartbeat
				stamp, parseErr := time.Parse(time.RFC3339Nano, heartbeat)
				age := time.Since(stamp)
				if parseErr == nil && age >= 0 && age <= 90*time.Second {
					target["online"] = "true"
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				target["desktop_status"] = "status_unavailable"
			}
			var queued, running int
			now := time.Now().Unix()
			if err := db.QueryRowContext(r.Context(),
				"SELECT COUNT(CASE WHEN status='queued' AND expires_at>? AND created_at+90>? THEN 1 END), COUNT(CASE WHEN status='running' AND expires_at>? AND lease_until>? THEN 1 END) FROM desktop_chat_jobs WHERE node_id=? AND workspace_id=? AND status IN ('queued','running')",
				now, now, now, now, target["node_id"], target["workspace_id"],
			).Scan(&queued, &running); err == nil {
				target["chat_queued"] = strconv.Itoa(queued)
				target["chat_running"] = strconv.Itoa(running)
				target["chat_jobs_status"] = "ready"
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) linkageImportTarget(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var setup linkage.SetupPackage
	if err := decoder.Decode(&setup); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid desktop linkage setup package")
		return
	}
	result, err := s.linkage.ImportSetup(r.Context(), setup)
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "linkage setup could not be persisted"
		switch err.Error() {
		case "invalid_linkage_setup_package", "invalid_linkage_setup_key", "linkage_address_must_be_private_ipv4", "backend_base_url_must_be_private_ipv4":
			status = http.StatusBadRequest
			message = err.Error()
		case "linkage_target_limit_reached":
			status = http.StatusTooManyRequests
			message = err.Error()
		default:
			if strings.Contains(err.Error(), "conflict") {
				status = http.StatusConflict
				message = err.Error()
			}
		}
		writeError(w, status, message)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) linkageSubmit(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var command linkage.Command
	if err := decoder.Decode(&command); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "linkage command exceeds 64 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid linkage command")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "invalid linkage command")
		return
	}
	if command.OperationID != "" && command.SchemaVersion == 1 && command.Type == "WATCHLIST_ADD" &&
		command.InstrumentType == "stock" && strings.TrimSpace(command.Symbol) != "" {
		_, lookupErr := s.linkage.GetOperation(r.Context(), command.OperationID)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			if validationErr := s.validateStockDirectorySymbol(r.Context(), command.Symbol); validationErr != nil {
				status := http.StatusBadRequest
				if strings.HasPrefix(validationErr.Error(), "stock_directory_") {
					status = http.StatusServiceUnavailable
				}
				writeError(w, status, validationErr.Error())
				return
			}
		}
	}
	operation, replayed, err := s.linkage.Submit(r.Context(), command)
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "linkage storage is unavailable"
		switch {
		case err.Error() == "operation_id_conflict":
			status = http.StatusConflict
			message = err.Error()
		case err.Error() == "linkage_queue_full":
			status = http.StatusTooManyRequests
			message = err.Error()
		case isLinkageValidationError(err.Error()):
			status = http.StatusBadRequest
			message = err.Error()
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status = http.StatusRequestTimeout
			message = "linkage request canceled"
		}
		if status == http.StatusBadRequest && s.logger != nil {
			s.logger.Printf("level=warn event=linkage_command_rejected request_id=%q reason=%q", command.RequestID, err)
		}
		writeError(w, status, message)
		return
	}
	status := http.StatusAccepted
	if replayed {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"operation": operation, "replayed": replayed})
}

func isLinkageValidationError(code string) bool {
	switch code {
	case "unsupported_command", "invalid_request_id", "invalid_target", "unsupported_symbol",
		"invalid_created_at", "invalid_expires_at", "command_expired", "invalid_context",
		"invalid_context_refs", "invalid_observed_trade_date", "wrong_target", "duplicate_target":
		return true
	default:
		return false
	}
}

func (s *Server) linkageOperation(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	operationID := strings.TrimSpace(r.PathValue("operation_id"))
	if operationID == "" {
		writeError(w, http.StatusBadRequest, "invalid operation id")
		return
	}
	operation, err := s.linkage.GetOperation(r.Context(), operationID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "linkage operation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load linkage operation")
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

// The shared default changes only through the administrator control boundary.
func (s *Server) linkageDefaultUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authorizedLinkageAdmin(r) || (r.Header.Get("Origin") != "" && !webSameOrigin(r)) {
		writeError(w, http.StatusForbidden, "需要管理员权限")
		return
	}
	var input struct {
		Node string `json:"node_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, 400, "无效默认桌面设置")
		return
	}
	input.Node = strings.TrimSpace(input.Node)
	if input.Node != "" {
		found := false
		if s.linkage != nil {
			for _, target := range s.linkage.Targets() {
				if target["node_id"] == input.Node {
					found = true
					break
				}
			}
		}
		if !found {
			writeError(w, 400, "桌面节点未登记")
			return
		}
	}
	if _, err := s.settingsStore.Update(func(values *appsettings.Values) error { values.LinkageDefaultNodeID = input.Node; return nil }); err != nil {
		writeError(w, 503, "默认桌面保存失败")
		return
	}
	writeJSON(w, 200, map[string]any{"node_id": input.Node})
}
