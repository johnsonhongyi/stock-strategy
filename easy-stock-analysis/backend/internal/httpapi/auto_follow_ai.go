package httpapi

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"easy-stock/backend/internal/runtimelog"
)

// The linkage middleware verifies HMAC before this identity-scoped queue route.
func (s *Server) autoFollowAIPeer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		NodeID      string          `json:"node_id"`
		WorkspaceID string          `json:"workspace_id"`
		ID          string          `json:"id"`
		LeaseToken  string          `json:"lease_token"`
		Status      string          `json:"status"`
		Detail      json.RawMessage `json:"detail"`
		Result      json.RawMessage `json:"result"`
		WaitMS      int             `json:"wait_ms"`
		PreferChat  bool            `json:"prefer_chat"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 192<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, 400, "invalid AI peer message")
		return
	}
	if s.linkage == nil || input.NodeID == "" || input.NodeID != r.Header.Get("X-Linkage-Node-ID") || input.WorkspaceID != r.Header.Get("X-Linkage-Workspace-ID") || !s.linkage.IsConfiguredTarget(input.WorkspaceID, input.NodeID) {
		writeError(w, 403, "signed configured desktop identity required")
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/api/v1/linkage/strategy/")
	if operation == "next" {
		s.desktopNext(w, r, input.NodeID, input.WorkspaceID, input.WaitMS, input.PreferChat)
		return
	}
	if operation == "chat_claim" || operation == "chat_result" {
		db, err := s.desktopDB()
		if err != nil {
			writeError(w, 503, "desktop chat unavailable")
			return
		}
		response, err := desktopChatPeer(db, operation, input.NodeID, input.WorkspaceID, input.ID, input.LeaseToken, input.Status, input.Result)
		if err != nil {
			writeDesktopPeerError(w, err)
			return
		}
		s.desktopSignals.notify("chat:" + input.ID)
		writeJSON(w, 200, response)
		return
	}
	db, err := s.desktopDB()
	if err != nil {
		writeError(w, 503, "AI queue unavailable")
		return
	}
	now := time.Now()
	at := now.UTC().Format(time.RFC3339Nano)
	switch strings.TrimPrefix(r.URL.Path, "/api/v1/linkage/strategy/") {
	case "lease":
		var expires string
		if err = db.QueryRowContext(r.Context(), "SELECT expires_at FROM ai_jobs WHERE id=? AND node_id=? AND workspace_id=? AND status='running' AND lease_token=? AND CAST(lease_until AS INTEGER)>?", input.ID, input.NodeID, input.WorkspaceID, input.LeaseToken, now.Unix()).Scan(&expires); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeDesktopPeerError(w, desktopPeerFailure{"STRATEGY_LEASE_INACTIVE", 409})
			} else {
				writeDesktopPeerError(w, err)
			}
			return
		}
		expiry, e := time.Parse(time.RFC3339Nano, expires)
		if e != nil || !expiry.After(now) {
			writeDesktopPeerError(w, desktopPeerFailure{"STRATEGY_EXPIRED", 409})
			return
		}
		updated, e := db.ExecContext(r.Context(), "UPDATE ai_jobs SET lease_until=? WHERE id=? AND node_id=? AND workspace_id=? AND status='running' AND lease_token=? AND CAST(lease_until AS INTEGER)>?", now.Add(360*time.Second).Unix(), input.ID, input.NodeID, input.WorkspaceID, input.LeaseToken, now.Unix())
		if e != nil {
			writeDesktopPeerError(w, e)
			return
		}
		n, _ := updated.RowsAffected()
		if n != 1 {
			writeDesktopPeerError(w, desktopPeerFailure{"STRATEGY_LEASE_INACTIVE", 409})
			return
		}
		writeJSON(w, 200, map[string]any{"status": "stored"})
	case "heartbeat":
		if (input.Status != "ready" && input.Status != "analyzing" && input.Status != "degraded") || len(input.Detail) > 16384 {
			writeError(w, 400, "invalid desktop status")
			return
		}
		detail := string(input.Detail)
		if detail == "" {
			detail = "{}"
		}
		_, err = db.ExecContext(r.Context(), "INSERT INTO ai_desktop(node_id,workspace_id,heartbeat,status,detail) VALUES(?,?,?,?,?) ON CONFLICT(node_id,workspace_id) DO UPDATE SET heartbeat=excluded.heartbeat,status=excluded.status,detail=excluded.detail", input.NodeID, input.WorkspaceID, at, input.Status, detail)
		if err != nil {
			writeError(w, 503, "desktop heartbeat unavailable")
			return
		}
		if err := s.reconcileStrategyBridge(r.Context(), db); err != nil && s.logger != nil {
			s.logger.Printf("level=warn event=strategy_bridge_reconcile error=%q", runtimelog.Redact(err.Error()))
		}
		writeJSON(w, 200, map[string]any{"status": "stored"})
	case "claim":
		response, e := s.claimAutomaticResearch(r.Context(), db, input.NodeID, input.WorkspaceID)
		if e != nil {
			writeDesktopPeerError(w, e)
			return
		}
		writeJSON(w, 200, response)
	case "result":
		if len(input.ID) != 32 || len(input.LeaseToken) != 64 || (input.Status != "completed" && input.Status != "failed") || len(input.Result) == 0 || !json.Valid(input.Result) {
			writeError(w, 400, "invalid AI result")
			return
		}
		var status, token, expires string
		var previous sql.NullString
		var leaseUntil int64
		err = db.QueryRowContext(r.Context(), "SELECT status,COALESCE(lease_token,''),expires_at,result_hash,CAST(COALESCE(lease_until,'0') AS INTEGER) FROM ai_jobs WHERE id=? AND node_id=? AND workspace_id=?", input.ID, input.NodeID, input.WorkspaceID).Scan(&status, &token, &expires, &previous, &leaseUntil)
		if err != nil {
			writeError(w, 404, "AI job not assigned to desktop")
			return
		}
		var compact bytes.Buffer
		_ = json.Compact(&compact, input.Result)
		hashed := sha256.Sum256(compact.Bytes())
		resultHash := hex.EncodeToString(hashed[:])
		if status == input.Status && previous.String == resultHash && token == input.LeaseToken {
			if err = settleAutomaticResearch(db, input.ID, input.Result); err != nil {
				writeError(w, 503, "usage settlement unavailable")
				return
			}
			writeJSON(w, 200, map[string]any{"status": "stored", "replayed": true})
			return
		}
		expiry, e := time.Parse(time.RFC3339Nano, expires)
		if status != "running" || token != input.LeaseToken || leaseUntil <= now.Unix() || e != nil || !expiry.After(now) {
			writeError(w, 409, "AI lease expired or result conflict")
			return
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(input.Result, &envelope) != nil || envelope == nil {
			writeError(w, 400, "invalid result envelope")
			return
		}
		var threadID string
		var proposal map[string]json.RawMessage
		if input.Status == "completed" && (json.Unmarshal(envelope["thread_id"], &threadID) != nil || len(threadID) < 1 || len(threadID) > 128 || json.Unmarshal(envelope["proposal"], &proposal) != nil || proposal == nil || len(proposal["plans"]) == 0) {
			writeError(w, 400, "AI proposal and thread identity required")
			return
		}
		updated, e := db.ExecContext(r.Context(), "UPDATE ai_jobs SET status=?,result=?,result_hash=?,finished_at=? WHERE id=? AND status='running' AND lease_token=? AND CAST(lease_until AS INTEGER)>?", input.Status, string(input.Result), resultHash, at, input.ID, token, now.Unix())
		if e != nil {
			writeError(w, 503, "AI result unavailable")
			return
		}
		n, _ := updated.RowsAffected()
		if n != 1 {
			writeError(w, 409, "AI lease conflict")
			return
		}
		if err = settleAutomaticResearch(db, input.ID, input.Result); err != nil {
			writeError(w, 503, "usage settlement unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "stored"})
	default:
		writeError(w, 404, "unknown AI peer operation")
	}
}
