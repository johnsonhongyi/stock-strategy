package httpapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"easy-stock/backend/internal/linkage"
)

var linkageEvidenceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
var linkageEvidenceSources = map[string]string{
	"tk.auction":         "TK 竞价",
	"tk.anomaly":         "TK 异动",
	"ats.leader":         "ATS 龙头角色",
	"ats.ipo":            "ATS 新股次新",
	"instock.candidates": "Instock 筛选候选",
}

type linkageEvidenceRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	NodeID      string          `json:"node_id"`
	SourceID    string          `json:"source_id"`
	SnapshotID  string          `json:"snapshot_id"`
	TradeDate   string          `json:"trade_date"`
	AsOf        string          `json:"as_of"`
	Payload     json.RawMessage `json:"payload"`
}

func (s *Server) linkageEvidenceHandler(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 640<<10))
	decoder.DisallowUnknownFields()
	var request linkageEvidenceRequest
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid context evidence")
		return
	}
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.NodeID = strings.TrimSpace(request.NodeID)
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.SnapshotID = strings.TrimSpace(request.SnapshotID)
	request.TradeDate = strings.TrimSpace(request.TradeDate)
	request.AsOf = strings.TrimSpace(request.AsOf)
	if !s.linkage.IsConfiguredTarget(request.WorkspaceID, request.NodeID) {
		writeError(w, http.StatusForbidden, "context evidence target is not configured")
		return
	}
	if _, ok := linkageEvidenceSources[request.SourceID]; !ok ||
		!linkageEvidenceIDPattern.MatchString(request.SnapshotID) || len(request.Payload) == 0 || len(request.Payload) > 512<<10 {
		writeError(w, http.StatusBadRequest, "invalid context evidence identity")
		return
	}
	if parsed, err := time.Parse("2006-01-02", request.TradeDate); err != nil || parsed.Format("2006-01-02") != request.TradeDate {
		writeError(w, http.StatusBadRequest, "invalid context evidence trade date")
		return
	}
	asOf, err := time.Parse(time.RFC3339Nano, request.AsOf)
	if err != nil {
		writeError(w, http.StatusBadRequest, "context evidence as_of must include a timezone")
		return
	}
	var fields map[string]json.RawMessage
	if !json.Valid(request.Payload) || json.Unmarshal(request.Payload, &fields) != nil || fields == nil {
		writeError(w, http.StatusBadRequest, "context evidence payload must be a JSON object")
		return
	}
	var schemaVersion int
	var sourceID string
	if json.Unmarshal(fields["schema_version"], &schemaVersion) != nil || schemaVersion != 1 ||
		json.Unmarshal(fields["source_id"], &sourceID) != nil || sourceID != request.SourceID {
		writeError(w, http.StatusBadRequest, "context evidence schema or source does not match")
		return
	}
	evidence := linkage.ContextEvidence{
		WorkspaceID: request.WorkspaceID, NodeID: request.NodeID, SourceID: request.SourceID,
		SnapshotID: request.SnapshotID, TradeDate: request.TradeDate, AsOf: asOf.UTC().Format(time.RFC3339Nano),
		Payload: request.Payload,
	}
	inserted, err := s.linkage.StoreContextEvidence(r.Context(), evidence)
	if err != nil {
		if err.Error() == "invalid_context_evidence" {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusServiceUnavailable, "context evidence storage is unavailable")
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"status": "stored", "inserted": inserted, "source_id": evidence.SourceID, "snapshot_id": evidence.SnapshotID})
}
