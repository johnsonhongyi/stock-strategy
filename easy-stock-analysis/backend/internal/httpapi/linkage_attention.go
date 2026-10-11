package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"easy-stock/backend/internal/linkage"
)

type linkageAttentionBatchItem struct {
	IntentID string `json:"intent_id"`
	Symbol   string `json:"symbol"`
	Source   string `json:"source"`
	Action   string `json:"action"`
}

type linkageAttentionBatchRequest struct {
	WorkspaceID  string                      `json:"workspace_id"`
	NodeID       string                      `json:"node_id"`
	BaseRevision int64                       `json:"base_revision"`
	Intents      []linkageAttentionBatchItem `json:"intents"`
}

func (s *Server) linkageAttentionSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if !s.linkage.IsConfiguredTarget(workspaceID, nodeID) {
		writeError(w, http.StatusForbidden, "attention target is not configured for this workspace")
		return
	}
	snapshot, err := s.linkage.GetAttentionSnapshot(r.Context(), workspaceID, nodeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attention identity")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) linkageAttentionIntent(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	var intent linkage.AttentionIntent
	if err := decoder.Decode(&intent); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid attention intent")
		return
	}
	if !s.linkage.IsConfiguredTarget(intent.WorkspaceID, intent.NodeID) {
		writeError(w, http.StatusForbidden, "attention target is not configured for this workspace")
		return
	}
	if strings.EqualFold(strings.TrimSpace(intent.Action), "add") {
		if validationErr := s.validateStockDirectorySymbol(r.Context(), intent.Symbol); validationErr != nil {
			status := http.StatusBadRequest
			if strings.HasPrefix(validationErr.Error(), "stock_directory_") {
				status = http.StatusServiceUnavailable
			}
			writeError(w, status, validationErr.Error())
			return
		}
	}
	result, err := s.linkage.ApplyAttentionIntent(r.Context(), intent)
	if err != nil {
		switch {
		case err.Error() == "attention_revision_conflict":
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": "attention_revision_conflict", "revision": result.Revision,
			})
		case err.Error() == "attention_intent_conflict":
			writeError(w, http.StatusConflict, "attention_intent_conflict")
		case strings.HasPrefix(err.Error(), "invalid_attention_"):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusRequestTimeout, "attention request canceled")
		default:
			writeError(w, http.StatusServiceUnavailable, "attention storage is unavailable")
		}
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (s *Server) linkageAttentionBatch(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	var batch linkageAttentionBatchRequest
	if err := decoder.Decode(&batch); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid attention batch")
		return
	}
	if len(batch.Intents) == 0 || len(batch.Intents) > 500 {
		writeError(w, http.StatusBadRequest, "attention batch must contain 1 to 500 intents")
		return
	}
	if !s.linkage.IsConfiguredTarget(batch.WorkspaceID, batch.NodeID) {
		writeError(w, http.StatusForbidden, "attention target is not configured for this workspace")
		return
	}
	snapshot, err := s.linkage.GetAttentionSnapshot(r.Context(), batch.WorkspaceID, batch.NodeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attention identity")
		return
	}
	if snapshot.Revision != batch.BaseRevision {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "attention_revision_conflict", "revision": snapshot.Revision})
		return
	}
	results := make([]linkage.AttentionIntentResult, 0, len(batch.Intents))
	failed := make([]map[string]string, 0)
	revision := snapshot.Revision
	for _, item := range batch.Intents {
		if strings.EqualFold(strings.TrimSpace(item.Action), "add") {
			if validationErr := s.validateStockDirectorySymbol(r.Context(), item.Symbol); validationErr != nil {
				failed = append(failed, map[string]string{"intent_id": item.IntentID, "code": validationErr.Error()})
				continue
			}
		}
		intent := linkage.AttentionIntent{IntentID: item.IntentID, WorkspaceID: batch.WorkspaceID,
			NodeID: batch.NodeID, Symbol: item.Symbol, Source: item.Source, Action: item.Action,
			BaseRevision: revision}
		result, applyErr := s.linkage.ApplyAttentionIntent(r.Context(), intent)
		if applyErr != nil {
			if applyErr.Error() == "attention_revision_conflict" {
				latest, snapshotErr := s.linkage.GetAttentionSnapshot(r.Context(), batch.WorkspaceID, batch.NodeID)
				if snapshotErr != nil {
					writeError(w, http.StatusServiceUnavailable, "attention storage is unavailable")
					return
				}
				writeJSON(w, http.StatusConflict, map[string]any{
					"code": "attention_revision_conflict", "revision": latest.Revision,
					"applied_count": len(results),
				})
				return
			}
			failed = append(failed, map[string]string{"intent_id": item.IntentID, "code": applyErr.Error()})
			continue
		}
		results = append(results, result)
		if result.Revision > revision {
			revision = result.Revision
		}
	}
	latest, err := s.linkage.GetAttentionSnapshot(r.Context(), batch.WorkspaceID, batch.NodeID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "attention storage is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 1, "revision": latest.Revision, "results": results, "errors": failed,
	})
}
