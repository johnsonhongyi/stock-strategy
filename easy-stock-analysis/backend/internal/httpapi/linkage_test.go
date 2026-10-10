package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"easy-stock/backend/internal/linkage"
)

func TestLinkageEventsLatestRouteReturnsNewestInChronologicalOrder(t *testing.T) {
	server := NewServer(Config{Token: "test-token"})
	service, err := linkage.NewService(filepath.Join(t.TempDir(), "linkage.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	server.linkage = service
	defer server.Close()

	events := []linkage.ContextEvent{
		{EventID: "event-1", EventType: "heat.rank_changed", TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:00:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)},
		{EventID: "event-2", EventType: "heat.rank_changed", TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:01:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)},
		{EventID: "event-3", EventType: "heat.rank_changed", TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:02:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)},
	}
	if err := service.StoreContextSnapshot(context.Background(), "snapshot-1", "2026-10-10",
		[]byte(`{"schema_version":1,"snapshot_id":"snapshot-1"}`), events); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/linkage/events?latest=true&limit=2", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("latest events status = %d: %s", response.Code, body)
	}
	first, second := strings.Index(body, `"event_id":"event-2"`), strings.Index(body, `"event_id":"event-3"`)
	if first < 0 || second < 0 || first >= second || strings.Contains(body, `"event_id":"event-1"`) {
		t.Fatalf("latest events were not the newest two in chronological order: %s", body)
	}
}

func TestLinkageRequiresConfiguredTokenAndOmitsCORS(t *testing.T) {
	server := NewServer(Config{})
	defer server.Close()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/linkage/commands", nil)
	req.Header.Set("Origin", "https://example.invalid")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("linkage route unexpectedly returned CORS headers: %v", rec.Header())
	}
}

func TestLinkageAttentionRoutesRequireAuthAndPersistIntent(t *testing.T) {
	server := NewServer(Config{Token: "test-token"})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "key.bin"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	targetsPath := filepath.Join(root, "targets.json")
	targets := `{"targets":[{"node_id":"tk-1","workspace_id":"desk","label":"TK desktop","base_url":"http://127.0.0.1:26676","key_file":"key.bin","enabled":true},{"node_id":"ats-1","workspace_id":"desk","label":"ATS desktop","base_url":"http://127.0.0.1:26677","key_file":"key.bin","enabled":true}]}`
	if err := os.WriteFile(targetsPath, []byte(targets), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := linkage.NewService(filepath.Join(root, "linkage.db"), targetsPath)
	if err != nil {
		t.Fatal(err)
	}
	server.linkage = service
	defer server.Close()

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet,
		"/api/v1/linkage/attention?workspace_id=desk&node_id=tk-1", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	body := `{"intent_id":"intent-1","workspace_id":"desk","node_id":"tk-1","symbol":"600000.SH","source":"stock-strategy:sbc","action":"add","base_revision":0}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/attention/intents", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("intent response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet,
		"/api/v1/linkage/attention?workspace_id=desk&node_id=tk-1", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "stock-strategy:sbc") {
		t.Fatalf("snapshot response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet,
		"/api/v1/linkage/attention?workspace_id=desk&node_id=ats-1", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "stock-strategy:sbc") ||
		!strings.Contains(response.Body.String(), `"node_id":"ats-1"`) {
		t.Fatalf("cross-node snapshot response = %d %s", response.Code, response.Body.String())
	}
}

func TestLinkageEvidenceRouteRequiresConfiguredTargetAndDeduplicates(t *testing.T) {
	server := NewServer(Config{Token: "test-token"})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "key.bin"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	targetsPath := filepath.Join(root, "targets.json")
	targets := `{"targets":[{"node_id":"tk-1","workspace_id":"desk","label":"Test desktop","base_url":"http://127.0.0.1:26676","key_file":"key.bin","enabled":true}]}`
	if err := os.WriteFile(targetsPath, []byte(targets), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := linkage.NewService(filepath.Join(root, "linkage.db"), targetsPath)
	if err != nil {
		t.Fatal(err)
	}
	server.linkage = service
	defer server.Close()

	body := `{"workspace_id":"desk","node_id":"tk-1","source_id":"tk.auction","snapshot_id":"auction-1","trade_date":"2026-10-10","as_of":"2026-10-10T09:25:00+08:00","payload":{"schema_version":1,"source_id":"tk.auction","state":"NEUTRAL"}}`
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/linkage/context/evidence", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthorized.Code)
	}

	post := func(payload string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/context/evidence", strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	if response := post(body); response.Code != http.StatusCreated {
		t.Fatalf("first upload = %d %s", response.Code, response.Body.String())
	}
	if response := post(body); response.Code != http.StatusOK {
		t.Fatalf("replay upload = %d %s", response.Code, response.Body.String())
	}
	candidateBody := strings.ReplaceAll(body, `"source_id":"tk.auction"`, `"source_id":"instock.candidates"`)
	candidateBody = strings.Replace(candidateBody, `"state":"NEUTRAL"`, `"candidates":[]`, 1)
	if response := post(candidateBody); response.Code != http.StatusCreated {
		t.Fatalf("Instock candidate evidence upload = %d %s", response.Code, response.Body.String())
	}
	if response := post(strings.Replace(body, `"source_id":"tk.auction","state"`, `"source_id":"ats.ipo","state"`, 1)); response.Code != http.StatusBadRequest {
		t.Fatalf("source mismatch = %d %s", response.Code, response.Body.String())
	}
}

func TestLinkageTargetRouteRequiresBearerWhenTokenConfigured(t *testing.T) {
	server := NewServer(Config{Token: "test-token"})
	defer server.Close()

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/linkage/targets", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/linkage/targets", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not configured") {
		t.Fatalf("configured-token response = %d %s", response.Code, response.Body.String())
	}
}

func TestLinkageContextRouteRequiresBearerWhenTokenConfigured(t *testing.T) {
	server := NewServer(Config{Token: "test-token"})
	defer server.Close()

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/linkage/context", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/linkage/context", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not configured") {
		t.Fatalf("configured-token response = %d %s", response.Code, response.Body.String())
	}
}
