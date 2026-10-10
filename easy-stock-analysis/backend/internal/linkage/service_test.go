package linkage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitPersistsAndSignsDesktopDelivery(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifySignature(t, r, body, key)
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"op-1","status":"accepted"}`)
			return
		}
		_, _ = io.WriteString(w, `{"operation_id":"op-1","request_id":"req-1","status":"applied","result":{"saved":{"persisted":true,"favorites_revision":7},"consumers":{"TK":{"status":"synced","quote_status":"ready"},"ATS":{"status":"synced","quote_status":"ready"}}}}`)
	}))
	defer peer.Close()

	registryPath := filepath.Join(root, "targets.json")
	registry, _ := json.Marshal(targetRegistry{Targets: []Target{{
		NodeID: "desktop-1", WorkspaceID: "workspace-1", Label: "Test desktop",
		BaseURL: peer.URL, KeyFile: keyPath, Enabled: true,
	}}})
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	now := time.Now().UTC()
	command := Command{
		SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "req-1", OperationID: "op-1",
		WorkspaceID: "workspace-1", TargetNodeIDs: []string{"desktop-1"}, Symbol: "600733.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", Reason: "SBC analysis",
		ContextRefs: []string{"sbc:test"}, CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(4 * time.Minute).Format(time.RFC3339),
	}
	first, replayed, err := service.Submit(t.Context(), command)
	if err != nil || replayed || first.OperationID != command.OperationID {
		t.Fatalf("first submit = %+v, replayed=%t, err=%v", first, replayed, err)
	}
	second, replayed, err := service.Submit(t.Context(), command)
	if err != nil || !replayed || second.OperationID != command.OperationID {
		t.Fatalf("replay = %+v, replayed=%t, err=%v", second, replayed, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(t.Context(), command.OperationID)
		if err == nil && len(operation.Deliveries) == 1 && operation.Deliveries[0].Status == "applied" &&
			strings.Contains(string(operation.Deliveries[0].Result), `"quote_status":"ready"`) {
			if posts.Load() != 1 {
				t.Fatalf("desktop POST count = %d, want one idempotent delivery", posts.Load())
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("desktop delivery result did not reach the durable operation")
}

func TestAcceptedDeliveryKeepsPollingUntilApplied(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var posts, gets atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"accepted-op","status":"accepted"}`)
			return
		}
		if gets.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"operation_id":"accepted-op","status":"accepted"}`)
			return
		}
		_, _ = io.WriteString(w, `{"operation_id":"accepted-op","status":"applied","result":{"saved":{"persisted":true},"consumers":{"TK":{"status":"synced"},"ATS":{"status":"synced"}}}}`)
	}))
	defer peer.Close()

	registryPath := filepath.Join(root, "targets.json")
	registry, err := json.Marshal(targetRegistry{Targets: []Target{{
		NodeID: "desktop-1", WorkspaceID: "workspace-1", BaseURL: peer.URL, KeyFile: keyPath, Enabled: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	now := time.Now().UTC()
	command := Command{
		SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "accepted-req", OperationID: "accepted-op",
		WorkspaceID: "workspace-1", TargetNodeIDs: []string{"desktop-1"}, Symbol: "600733.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339),
	}
	if _, _, err := service.Submit(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(t.Context(), command.OperationID)
		if err == nil && len(operation.Deliveries) == 1 && operation.Deliveries[0].Status == "applied" {
			if posts.Load() != 1 || gets.Load() < 2 {
				t.Fatalf("desktop requests = POST %d / GET %d, want one POST and at least two GETs", posts.Load(), gets.Load())
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("accepted desktop operation did not continue polling: POST %d / GET %d", posts.Load(), gets.Load())
}

func TestSubmitRejectsOperationIDReuseWithDifferentCommand(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifySignature(t, r, body, key)
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"conflict-op","status":"accepted"}`)
			return
		}
		_, _ = io.WriteString(w, `{"operation_id":"conflict-op","status":"applied","result":{"consumers":{"TK":{"status":"synced"},"ATS":{"status":"synced"}}}}`)
	}))
	defer peer.Close()
	registryPath := filepath.Join(root, "targets.json")
	registry, err := json.Marshal(targetRegistry{Targets: []Target{{
		NodeID: "desktop-1", WorkspaceID: "workspace-1", BaseURL: peer.URL, KeyFile: keyPath, Enabled: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	now := time.Now().UTC()
	command := Command{SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "conflict-req", OperationID: "conflict-op",
		WorkspaceID: "workspace-1", TargetNodeIDs: []string{"desktop-1"}, Symbol: "600733.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}
	if _, replayed, err := service.Submit(t.Context(), command); err != nil || replayed {
		t.Fatalf("initial submit replayed=%t, err=%v", replayed, err)
	}
	command.Symbol = "600734.SH"
	if _, _, err := service.Submit(t.Context(), command); err == nil || err.Error() != "operation_id_conflict" {
		t.Fatalf("different command reuse error = %v, want operation_id_conflict", err)
	}
	if posts.Load() > 1 {
		t.Fatalf("operation ID reuse caused %d desktop POSTs", posts.Load())
	}
}

func TestSubmitKeepsMultiTargetDeliveryResultsIndependent(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var successfulPosts, failedPosts atomic.Int32
	successfulPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifySignature(t, r, body, key)
		if r.Method == http.MethodPost {
			successfulPosts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"multi-op","status":"accepted"}`)
			return
		}
		_, _ = io.WriteString(w, `{"operation_id":"multi-op","status":"applied","result":{"saved":{"persisted":true},"consumers":{"TK":{"status":"synced"},"ATS":{"status":"synced"}}}}`)
	}))
	defer successfulPeer.Close()
	failedPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifySignature(t, r, body, key)
		failedPosts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"target_rejected"}`)
	}))
	defer failedPeer.Close()

	registryPath := filepath.Join(root, "targets.json")
	registry, err := json.Marshal(targetRegistry{Targets: []Target{
		{NodeID: "desktop-a", WorkspaceID: "workspace-1", BaseURL: successfulPeer.URL, KeyFile: keyPath, Enabled: true},
		{NodeID: "desktop-b", WorkspaceID: "workspace-1", BaseURL: failedPeer.URL, KeyFile: keyPath, Enabled: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	now := time.Now().UTC()
	command := Command{SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "multi-req", OperationID: "multi-op",
		WorkspaceID: "workspace-1", TargetNodeIDs: []string{"desktop-a", "desktop-b"}, Symbol: "600733.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}
	if _, _, err := service.Submit(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(t.Context(), command.OperationID)
		if err == nil && len(operation.Deliveries) == 2 {
			byNode := make(map[string]Delivery, len(operation.Deliveries))
			for _, delivery := range operation.Deliveries {
				byNode[delivery.NodeID] = delivery
			}
			if byNode["desktop-a"].Status == "applied" && byNode["desktop-b"].Status == "failed" {
				if successfulPosts.Load() != 1 || failedPosts.Load() != 1 || byNode["desktop-a"].Error != "" ||
					byNode["desktop-b"].Error != "desktop_http_400" {
					t.Fatalf("target results were coupled: posts=%d/%d deliveries=%+v", successfulPosts.Load(), failedPosts.Load(), byNode)
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("multi-target delivery results did not settle independently")
}

func TestAppliedDeliveryReconcilesPendingConsumerReceipt(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var posts, gets atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"reconcile-op","status":"accepted"}`)
			return
		}
		if gets.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"operation_id":"reconcile-op","status":"applied","result":{"saved":{"persisted":true},"consumers":{"TK":{"status":"synced"},"ATS":{"status":"pending"}}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"operation_id":"reconcile-op","status":"applied","result":{"saved":{"persisted":true},"consumers":{"TK":{"status":"synced"},"ATS":{"status":"synced"}}}}`)
	}))
	defer peer.Close()

	registryPath := filepath.Join(root, "targets.json")
	registry, err := json.Marshal(targetRegistry{Targets: []Target{{
		NodeID: "desktop-1", WorkspaceID: "workspace-1", BaseURL: peer.URL, KeyFile: keyPath, Enabled: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	now := time.Now().UTC()
	command := Command{
		SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "reconcile-req", OperationID: "reconcile-op",
		WorkspaceID: "workspace-1", TargetNodeIDs: []string{"desktop-1"}, Symbol: "600733.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339),
	}
	if _, _, err := service.Submit(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(t.Context(), command.OperationID)
		if err == nil && len(operation.Deliveries) == 1 &&
			strings.Contains(string(operation.Deliveries[0].Result), `"ATS":{"status":"synced"}`) {
			if posts.Load() != 1 || gets.Load() < 2 {
				t.Fatalf("desktop requests = POST %d / GET %d, want one POST and at least two GETs", posts.Load(), gets.Load())
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("durable delivery result did not reconcile the ATS receipt")
}

func TestAppliedDeliveryPreservesReceiptDuringTransientGatewayFailure(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var posts, gets atomic.Int32
	failureReturned := make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifySignature(t, r, body, key)
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"reconcile-outage-op","status":"accepted"}`)
			return
		}
		switch gets.Add(1) {
		case 1:
			_, _ = io.WriteString(w, `{"operation_id":"reconcile-outage-op","status":"applied","result":{"saved":{"persisted":true},"consumers":{"TK":{"status":"synced"},"ATS":{"status":"pending"}}}}`)
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"temporary gateway failure"}`)
			close(failureReturned)
		default:
			_, _ = io.WriteString(w, `{"operation_id":"reconcile-outage-op","status":"applied","result":{"saved":{"persisted":true},"consumers":{"TK":{"status":"synced"},"ATS":{"status":"synced"}}}}`)
		}
	}))
	defer peer.Close()

	registryPath := filepath.Join(root, "targets.json")
	registry, err := json.Marshal(targetRegistry{Targets: []Target{{
		NodeID: "desktop-1", WorkspaceID: "workspace-1", BaseURL: peer.URL, KeyFile: keyPath, Enabled: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	now := time.Now().UTC()
	command := Command{
		SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "reconcile-outage-req", OperationID: "reconcile-outage-op",
		WorkspaceID: "workspace-1", TargetNodeIDs: []string{"desktop-1"}, Symbol: "600733.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339),
	}
	if _, _, err := service.Submit(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failureReturned:
	case <-time.After(6 * time.Second):
		t.Fatal("desktop did not return the simulated transient failure")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(t.Context(), command.OperationID)
		if err == nil && len(operation.Deliveries) == 1 && operation.Deliveries[0].Error == "desktop_http_503" {
			delivery := operation.Deliveries[0]
			if delivery.Status != "applied" || !strings.Contains(string(delivery.Result), `"persisted":true`) ||
				!strings.Contains(string(delivery.Result), `"ATS":{"status":"pending"}`) {
				t.Fatalf("transient failure lost the applied receipt: %+v", delivery)
			}
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(t.Context(), command.OperationID)
		if err == nil && len(operation.Deliveries) == 1 &&
			strings.Contains(string(operation.Deliveries[0].Result), `"ATS":{"status":"synced"}`) {
			if posts.Load() != 1 || gets.Load() < 3 {
				t.Fatalf("desktop requests = POST %d / GET %d, want one POST and at least three GETs", posts.Load(), gets.Load())
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("consumer receipt did not recover after the transient gateway failure")
}

func TestExpiredPendingDeliveryRecoversDesktopOperationAfterLostAcknowledgement(t *testing.T) {
	root := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	keyPath := filepath.Join(root, "peer.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	var posts, gets atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifySignature(t, r, body, key)
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"ack-op","status":"accepted"}`)
			return
		}
		gets.Add(1)
		_, _ = io.WriteString(w, `{"operation_id":"ack-op","request_id":"ack-req","status":"applied","result":{"saved":{"persisted":true,"favorites_revision":8},"consumers":{"TK":{"status":"synced","quote_status":"ready"},"ATS":{"status":"synced","quote_status":"ready"}}}}`)
	}))
	defer peer.Close()

	registryPath := filepath.Join(root, "targets.json")
	registry, err := json.Marshal(targetRegistry{Targets: []Target{{
		NodeID: "desktop-1", WorkspaceID: "workspace-1", BaseURL: peer.URL, KeyFile: keyPath, Enabled: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(filepath.Join(root, "linkage.db"), registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	expiredAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	command := Command{OperationID: "ack-op", RequestID: "ack-req", WorkspaceID: "workspace-1",
		TargetNodeIDs: []string{"desktop-1"}, Symbol: "600733.SH", Source: "stock-strategy.sbc",
		SchemaVersion: 1, Type: "WATCHLIST_ADD", InstrumentType: "stock", ExpiresAt: expiredAt}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`
		INSERT INTO linkage_operations(operation_id,request_id,command_hash,command_json,created_at,expires_at,updated_at)
		VALUES('ack-op','ack-req','hash',?,'2026-10-10T00:00:00Z',?,'2026-10-10T00:00:00Z');
		INSERT INTO linkage_deliveries(operation_id,node_id,workspace_id,status,next_attempt,updated_at)
		VALUES('ack-op','desktop-1','workspace-1','pending',9223372036854775807,'2026-10-10T00:00:00Z');`,
		string(commandJSON), expiredAt); err != nil {
		t.Fatal(err)
	}

	// A durable pending row represents a POST whose acknowledgement was lost.
	service.deliver("ack-op", "desktop-1", "pending", 1, string(commandJSON), expiredAt)
	operation, err := service.GetOperation(t.Context(), "ack-op")
	if err != nil || len(operation.Deliveries) != 1 || operation.Deliveries[0].Status != "applied" {
		t.Fatalf("recovered operation = %+v, err=%v", operation, err)
	}
	if posts.Load() != 0 || gets.Load() != 1 {
		t.Fatalf("desktop requests = POST %d / GET %d, want POST 0 / GET 1", posts.Load(), gets.Load())
	}
}

func TestDesktopDeliveryStatusMapsTerminalResults(t *testing.T) {
	for input, want := range map[string]string{
		"accepted": "accepted", "processing": "accepted", "": "accepted",
		"applied": "applied", "already_present": "already_present",
		"rejected": "rejected", "expired": "expired", "failed": "failed",
	} {
		if got := desktopDeliveryStatus(input); got != want {
			t.Errorf("desktopDeliveryStatus(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateCommandRejectsPublicAndUnknownTargets(t *testing.T) {
	if _, err := loadTargets(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal(err)
	}
	command := Command{SchemaVersion: 1, Type: "WATCHLIST_ADD", RequestID: "req", OperationID: "op",
		WorkspaceID: "workspace", TargetNodeIDs: []string{"desktop"}, Symbol: "600000.SH",
		InstrumentType: "stock", Source: "stock-strategy.sbc", CreatedAt: time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339)}
	if err := validateCommand(command, map[string]Target{}); err == nil || err.Error() != "wrong_target" {
		t.Fatalf("validateCommand error = %v, want wrong_target", err)
	}
}

func verifySignature(t *testing.T, request *http.Request, body, key []byte) {
	t.Helper()
	timestamp := request.Header.Get("X-Linkage-Timestamp")
	nonce := request.Header.Get("X-Linkage-Nonce")
	node := request.Header.Get("X-Linkage-Node-ID")
	workspace := request.Header.Get("X-Linkage-Workspace-ID")
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{request.Method, request.URL.RequestURI(), hex.EncodeToString(bodyHash[:]), timestamp, nonce, node, workspace}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(canonical))
	if got, want := request.Header.Get("X-Linkage-Signature"), fmt.Sprintf("%x", mac.Sum(nil)); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}
