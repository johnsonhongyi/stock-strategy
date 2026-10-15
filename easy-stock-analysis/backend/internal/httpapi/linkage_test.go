package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easy-stock/backend/internal/appsettings"
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
		{EventID: "event-1", EventType: "heat.rank_changed", CausationID: "snapshot-1", TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:00:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)},
		{EventID: "event-2", EventType: "heat.rank_changed", CausationID: "snapshot-1", TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:01:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)},
		{EventID: "event-3", EventType: "heat.rank_changed", CausationID: "snapshot-1", TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:02:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)},
	}
	if err := service.StoreContextSnapshot(context.Background(), "snapshot-1", "2026-10-10",
		[]byte(`{"schema_version":1,"workspace_id":"","snapshot_id":"snapshot-1"}`), events); err != nil {
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

func TestLinkageOptionsRequestSupportsCORSPreflight(t *testing.T) {
	server := NewServer(Config{})
	defer server.Close()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/linkage/commands", nil)
	req.Header.Set("Origin", "https://example.invalid")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.invalid" {
		t.Fatalf("preflight did not return CORS headers: %v", rec.Header())
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "X-Linkage-Signature") {
		t.Fatalf("preflight omitted HMAC headers: %v", rec.Header())
	}
}

func TestLinkageSetupImportRequiresConfiguredToken(t *testing.T) {
	server := NewServer(Config{Token: "test-token"})
	defer server.Close()

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", strings.NewReader(`{}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated setup import status = %d", unauthorized.Code)
	}
	wrongToken := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", strings.NewReader(`{}`))
	wrongToken.Header.Set("Authorization", "Bearer wrong-token")
	wrongTokenResponse := httptest.NewRecorder()
	server.ServeHTTP(wrongTokenResponse, wrongToken)
	if wrongTokenResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong setup token status = %d", wrongTokenResponse.Code)
	}
	wrongHeaderToken := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", strings.NewReader(`{}`))
	wrongHeaderToken.Header.Set("X-A-Stock-Token", "wrong-token")
	wrongHeaderResponse := httptest.NewRecorder()
	server.ServeHTTP(wrongHeaderResponse, wrongHeaderToken)
	if wrongHeaderResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong X-A-Stock-Token status = %d", wrongHeaderResponse.Code)
	}

	queryToken := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import?token=test-token", strings.NewReader(`{}`))
	queryTokenResponse := httptest.NewRecorder()
	server.ServeHTTP(queryTokenResponse, queryToken)
	if queryTokenResponse.Code != http.StatusUnauthorized {
		t.Fatalf("query-string setup token status = %d", queryTokenResponse.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated setup import without linkage service = %d: %s", response.Code, response.Body.String())
	}
	xTokenRequest := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", strings.NewReader(`{}`))
	xTokenRequest.Header.Set("X-A-Stock-Token", "test-token")
	xTokenResponse := httptest.NewRecorder()
	server.ServeHTTP(xTokenResponse, xTokenRequest)
	if xTokenResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated X-A-Stock-Token import without linkage service = %d: %s", xTokenResponse.Code, xTokenResponse.Body.String())
	}
}

func TestLinkageSetupImportWithoutTokenCompletesTKHandshake(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	tk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setupBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read TK setup body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(setupBody, &payload); err != nil || payload["schema_version"] != float64(1) || payload["stock_strategy_base_url"] != "http://192.168.31.5:20081" {
			t.Errorf("unexpected TK configure payload: %s", string(setupBody))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		timestamp := r.Header.Get("X-Linkage-Timestamp")
		nodeID := r.Header.Get("X-Linkage-Node-ID")
		workspaceID := r.Header.Get("X-Linkage-Workspace-ID")
		nonce := r.Header.Get("X-Linkage-Nonce")
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/bridge/v1/configure" {
			t.Errorf("TK callback = %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bodyHash := sha256.Sum256(setupBody)
		canonical := strings.Join([]string{r.Method, r.URL.RequestURI(), hex.EncodeToString(bodyHash[:]), timestamp, nonce, nodeID, workspaceID}, "\n")
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(canonical))
		if nodeID != "tk-test" || workspaceID != "ws-test" || len(nonce) < 16 || !hmac.Equal([]byte(r.Header.Get("X-Linkage-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Errorf("TK rejected Go HMAC headers: node=%q workspace=%q", nodeID, workspaceID)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"configured","node_id":"tk-test","workspace_id":"ws-test","connection_test":"hmac_verified"}`)
	}))
	defer tk.Close()
	port := tk.Listener.Addr().(*net.TCPAddr).Port
	server := NewServer(Config{LinkageDBPath: filepath.Join(t.TempDir(), "linkage.db")})
	defer server.Close()
	setup := map[string]any{
		"schema_version": 1, "kind": "stock-standalone.system-linkage",
		"node_id": "tk-test", "workspace_id": "ws-test", "label": "TK test",
		"address": "127.0.0.1", "port": port, "hmac_key": hex.EncodeToString(key),
		"backend_base_url": "http://192.168.31.5:20081",
	}
	body, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", bytes.NewReader(body))
	req.RemoteAddr = "192.168.50.184:12345"
	req.Host = "192.168.50.60:20073"
	req.Header.Set("Origin", "http://192.168.50.60:20073")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", rec.Code, rec.Body.String())
	}
	var result linkage.SetupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.TargetRegistered || result.DesktopStatus != "connected" || result.DesktopError != "" {
		t.Fatalf("setup result = %+v", result)
	}
	for _, path := range []string{"/api/v1/linkage/targets", "/api/v1/linkage/context", "/api/v1/linkage/events", "/api/v1/linkage/attention?workspace_id=ws-test&node_id=tk-test"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = req.RemoteAddr
		request.Host = req.Host
		request.Header.Set("Origin", req.Header.Get("Origin"))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("browser read %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	// An invalid command must reach validation rather than fail authentication.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/commands", strings.NewReader(`{}`))
	request.RemoteAddr, request.Host = req.RemoteAddr, req.Host
	request.Header.Set("Origin", req.Header.Get("Origin"))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("browser command status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLinkageAttentionRoutesRequireAuthAndPersistIntent(t *testing.T) {
	provider := newFixedStockDirectoryProvider("600000.SH", "000001.SZ")
	server := NewServer(Config{Token: "test-token", StockDirectory: provider})
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

	for _, invalid := range []struct {
		intentID string
		symbol   string
	}{
		{intentID: "intent-etf", symbol: "510300.SH"},
		{intentID: "intent-wrong-exchange", symbol: "600000.SZ"},
		{intentID: "intent-unknown-stock", symbol: "600999.SH"},
		{intentID: "intent-index", symbol: "399001.SZ"},
		{intentID: "intent-b-share-sh", symbol: "900901.SH"},
		{intentID: "intent-b-share-sz", symbol: "200001.SZ"},
	} {
		invalidBody := `{"intent_id":"` + invalid.intentID + `","workspace_id":"desk","node_id":"tk-1","symbol":"` +
			invalid.symbol + `","source":"stock-strategy:sbc","action":"add","base_revision":1}`
		invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/attention/intents", strings.NewReader(invalidBody))
		invalidRequest.Header.Set("Authorization", "Bearer test-token")
		invalidResponse := httptest.NewRecorder()
		server.ServeHTTP(invalidResponse, invalidRequest)
		if invalidResponse.Code != http.StatusBadRequest || !strings.Contains(invalidResponse.Body.String(), "unsupported_symbol") {
			t.Fatalf("invalid stock %s response = %d %s", invalid.symbol, invalidResponse.Code, invalidResponse.Body.String())
		}
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

	server.stockDirectories.mu.Lock()
	server.stockDirectories.snapshot.expiresAt = time.Now().Add(-time.Second)
	server.stockDirectories.mu.Unlock()
	provider.err = errors.New("catalog offline")
	staleBody := `{"intent_id":"intent-stale-catalog","workspace_id":"desk","node_id":"tk-1","symbol":"000001.SZ","source":"stock-strategy:sbc","action":"add","base_revision":1}`
	staleRequest := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/attention/intents", strings.NewReader(staleBody))
	staleRequest.Header.Set("Authorization", "Bearer test-token")
	staleResponse := httptest.NewRecorder()
	server.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusServiceUnavailable || !strings.Contains(staleResponse.Body.String(), "stock_directory_stale") {
		t.Fatalf("stale directory response = %d %s", staleResponse.Code, staleResponse.Body.String())
	}

	command := linkage.Command{
		SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "cmd-etf", OperationID: "op-etf",
		WorkspaceID: "desk", TargetNodeIDs: []string{"tk-1"}, Symbol: "510300.SH",
		InstrumentType: "stock", Source: "stock-strategy.manual",
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
	}
	commandBody, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/commands", bytes.NewReader(commandBody))
	commandRequest.Header.Set("Authorization", "Bearer test-token")
	commandResponse := httptest.NewRecorder()
	server.ServeHTTP(commandResponse, commandRequest)
	if commandResponse.Code != http.StatusBadRequest || !strings.Contains(commandResponse.Body.String(), "unsupported_symbol") {
		t.Fatalf("ETF watchlist command response = %d %s", commandResponse.Code, commandResponse.Body.String())
	}
}

func TestLinkageAttentionBatchAllowsRemovingOutOfCatalogStock(t *testing.T) {
	server := NewServer(Config{Token: "test-token", StockDirectory: newFixedStockDirectoryProvider("600000.SH")})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "key.bin"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	targetsPath := filepath.Join(root, "targets.json")
	targets := `{"targets":[{"node_id":"tk-1","workspace_id":"desk","label":"TK desktop","base_url":"http://127.0.0.1:26676","key_file":"key.bin","enabled":true}]}`
	if err := os.WriteFile(targetsPath, []byte(targets), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := linkage.NewService(filepath.Join(root, "linkage.db"), targetsPath)
	if err != nil {
		t.Fatal(err)
	}
	server.linkage = service
	defer server.Close()
	if _, err := service.ApplyAttentionIntent(context.Background(), linkage.AttentionIntent{
		IntentID: "seed-old-etf", WorkspaceID: "desk", NodeID: "tk-1", Symbol: "510300.SH",
		Source: "manual", Action: "add", BaseRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	invalidAdd := `{"workspace_id":"desk","node_id":"tk-1","base_revision":1,"intents":[{"intent_id":"reject-new-etf","symbol":"510300.SH","source":"manual","action":"add"}]}`
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/attention/intents/batch", strings.NewReader(invalidAdd))
	invalidRequest.Header.Set("Authorization", "Bearer test-token")
	invalidResponse := httptest.NewRecorder()
	server.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusOK || !strings.Contains(invalidResponse.Body.String(), "unsupported_symbol") {
		t.Fatalf("batch ETF add response = %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}

	body := `{"workspace_id":"desk","node_id":"tk-1","base_revision":1,"intents":[{"intent_id":"remove-old-etf","symbol":"510300.SH","source":"manual","action":"remove_all"}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/attention/intents/batch", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"errors":[{`) {
		t.Fatalf("out-of-catalog removal response = %d %s", response.Code, response.Body.String())
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

func TestLinkageLANAccessBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, remote, host, origin, realIP string
		want                               bool
	}{
		{"same origin", "192.168.50.184:1234", "192.168.50.60:20073", "http://192.168.50.60:20073", "", true},
		{"LAN CLI", "192.168.50.184:1234", "192.168.50.60:20073", "", "", true},
		{"loopback CLI", "127.0.0.1:1234", "127.0.0.1:20081", "", "", true},
		{"nginx LAN", "172.18.0.3:1234", "192.168.50.60:20073", "http://192.168.50.60:20073", "192.168.50.184", true},
		{"public client", "203.0.113.1:1234", "192.168.50.60:20073", "", "", false},
		{"public proxy client", "172.18.0.3:1234", "192.168.50.60:20073", "", "203.0.113.1", false},
		{"cross origin", "192.168.50.184:1234", "192.168.50.60:20073", "https://untrusted.invalid", "", false},
		{"wrong port", "192.168.50.184:1234", "192.168.50.60:20073", "http://192.168.50.60:20074", "", false},
		{"null origin", "192.168.50.184:1234", "192.168.50.60:20073", "null", "", false},
		{"public host", "192.168.50.184:1234", "untrusted.invalid", "", "", false},
		{"missing remote", "", "192.168.50.60:20073", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/targets/import", nil)
			r.RemoteAddr, r.Host = tc.remote, tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.realIP != "" {
				r.Header.Set("X-Real-IP", tc.realIP)
			}
			server := &Server{}
			if got := server.authorizedLinkageAdmin(r); got != tc.want {
				t.Fatalf("admin allowed=%t want=%t", got, tc.want)
			}
			if got := server.authorizedLinkage(r); got != tc.want {
				t.Fatalf("browser allowed=%t want=%t", got, tc.want)
			}
			r.Header.Set("X-Linkage-Node-ID", "tk-spoof")
			if server.authorizedLinkage(r) {
				t.Fatal("unverified desktop identity bypassed HMAC")
			}
		})
	}
}

func TestLinkageDefaultPersistsAndRejectsUnknownNode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "targets.json")
	if err := os.WriteFile(filepath.Join(root, "key.bin"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"targets":[{"node_id":"tk-1","workspace_id":"desk","label":"JohnsonTriton","base_url":"http://127.0.0.1:26676","key_file":"key.bin","enabled":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := linkage.NewService(filepath.Join(root, "linkage.db"), path)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(root, "settings.json")
	store, err := appsettings.Open(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{Token: "test-token", SettingsStore: store})
	server.linkage = service
	defer server.Close()
	request := func(body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/v1/linkage/default", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(`{"node_id":"tk-1"}`, ""); rec.Code != 401 {
		t.Fatalf("unauthorized: %d", rec.Code)
	}
	if rec := request(`{"node_id":"tk-1"}`, "test-token"); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	reopened, err := appsettings.Open(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().LinkageDefaultNodeID != "tk-1" {
		t.Fatal("default not persisted")
	}
	if rec := request(`{"node_id":"missing"}`, "test-token"); rec.Code != 400 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if store.Snapshot().LinkageDefaultNodeID != "tk-1" {
		t.Fatal("invalid update changed default")
	}
	req := httptest.NewRequest("GET", "/api/v1/linkage/targets", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"default_node_id":"tk-1"`) {
		t.Fatalf("read: %s", rec.Body.String())
	}
	if rec := request(`{"node_id":""}`, "test-token"); rec.Code != 200 || store.Snapshot().LinkageDefaultNodeID != "" {
		t.Fatal("clear failed")
	}
}
