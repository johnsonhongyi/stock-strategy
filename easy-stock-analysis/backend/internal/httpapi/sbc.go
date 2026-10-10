package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"easy-stock/backend/internal/sbc"
)

// Analyze the exact displayed bars without fetching a different snapshot.
func (s *Server) sbcAnalysis(w http.ResponseWriter, r *http.Request) {
	var input sbc.Request
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SBC analysis request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "SBC requires a single JSON request")
		return
	}
	if err := sbc.Validate(input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload, err := json.Marshal(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid SBC bars")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := sbc.Analyze(ctx, payload)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sbc.ErrInvalidInput) {
			status = http.StatusBadRequest
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	if !json.Valid(result) {
		writeError(w, http.StatusInternalServerError, "SBC analysis returned invalid JSON")
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"data": result})
}
