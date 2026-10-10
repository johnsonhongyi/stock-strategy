package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"easy-stock/backend/internal/linkage"
)

func (s *Server) linkageTargets(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 1,
		"targets":        s.linkage.Targets(),
	})
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
