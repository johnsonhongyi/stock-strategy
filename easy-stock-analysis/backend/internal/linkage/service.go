package linkage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	maxCommandAge        = 5 * time.Minute
	deliveryPollInterval = 500 * time.Millisecond
)

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	nodePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,96}$`)
	symbolPattern = regexp.MustCompile(`^([0-9]{6})\.(SH|SZ)$`)
)

type Command struct {
	SchemaVersion     int      `json:"schema_version"`
	Type              string   `json:"type"`
	RequestID         string   `json:"request_id"`
	OperationID       string   `json:"operation_id"`
	WorkspaceID       string   `json:"workspace_id"`
	TargetNodeIDs     []string `json:"target_node_ids"`
	Symbol            string   `json:"symbol"`
	InstrumentType    string   `json:"instrument_type"`
	Source            string   `json:"source"`
	Reason            string   `json:"reason"`
	ContextRefs       []string `json:"context_refs"`
	ObservedTradeDate string   `json:"observed_trade_date,omitempty"`
	CreatedAt         string   `json:"created_at"`
	ExpiresAt         string   `json:"expires_at"`
}

type Target struct {
	NodeID      string `json:"node_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label,omitempty"`
	BaseURL     string `json:"base_url"`
	KeyFile     string `json:"key_file"`
	Enabled     bool   `json:"enabled"`
	key         []byte
	base        *url.URL
}

type targetRegistry struct {
	Targets []Target `json:"targets"`
}

type Delivery struct {
	NodeID      string          `json:"node_id"`
	WorkspaceID string          `json:"workspace_id"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	UpdatedAt   string          `json:"updated_at"`
}

type Operation struct {
	OperationID string     `json:"operation_id"`
	RequestID   string     `json:"request_id"`
	Status      string     `json:"status"`
	Deliveries  []Delivery `json:"deliveries"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

type ContextEvent struct {
	EventID           string          `json:"event_id"`
	EventType         string          `json:"event_type"`
	OriginNode        string          `json:"origin_node"`
	CausationID       string          `json:"causation_id"`
	SourceSnapshotIDs []string        `json:"source_snapshot_ids"`
	TradeDate         string          `json:"trade_date,omitempty"`
	OccurredAt        string          `json:"occurred_at"`
	Payload           json.RawMessage `json:"payload"`
}

type ContextEvidence struct {
	WorkspaceID string          `json:"workspace_id"`
	NodeID      string          `json:"node_id"`
	SourceID    string          `json:"source_id"`
	SnapshotID  string          `json:"snapshot_id"`
	TradeDate   string          `json:"trade_date"`
	AsOf        string          `json:"as_of"`
	ReceivedAt  string          `json:"received_at"`
	Payload     json.RawMessage `json:"payload"`
}

type Service struct {
	db             *sql.DB
	targetMu       sync.RWMutex
	targetWriteMu  sync.Mutex
	targets        map[string]Target
	client         *http.Client
	wake           chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	nonceCleanupMu sync.Mutex
	nonceCleanupAt time.Time
}

func NewService(databasePath, targetsPath string) (*Service, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("linkage database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create linkage database directory: %w", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open linkage database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS linkage_operations (
			operation_id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL UNIQUE,
			command_hash TEXT NOT NULL,
			command_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS linkage_deliveries (
			operation_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			status TEXT NOT NULL,
			result_json TEXT NOT NULL DEFAULT '{}',
			error TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(operation_id,node_id),
			FOREIGN KEY(operation_id) REFERENCES linkage_operations(operation_id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS linkage_context_snapshots (
			snapshot_id TEXT PRIMARY KEY,
			trade_date TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			payload BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_linkage_context_snapshots_created
			ON linkage_context_snapshots(created_at DESC);
        CREATE INDEX IF NOT EXISTS idx_linkage_context_workspace_created
            ON linkage_context_snapshots(CASE WHEN json_valid(CAST(payload AS TEXT)) THEN json_extract(CAST(payload AS TEXT),'$.workspace_id') END,created_at DESC,snapshot_id DESC);
		CREATE TABLE IF NOT EXISTS linkage_context_events (
			event_id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL,
			origin_node TEXT NOT NULL,
			causation_id TEXT NOT NULL,
			source_snapshot_ids TEXT NOT NULL,
			trade_date TEXT NOT NULL DEFAULT '',
			occurred_at TEXT NOT NULL,
			payload BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_linkage_context_events_occurred
			ON linkage_context_events(occurred_at,event_id);
        CREATE INDEX IF NOT EXISTS idx_linkage_context_events_causation_time
            ON linkage_context_events(causation_id,occurred_at,event_id);
		CREATE TABLE IF NOT EXISTS linkage_context_evidence (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			snapshot_id TEXT NOT NULL,
			trade_date TEXT NOT NULL,
			as_of TEXT NOT NULL,
			received_at TEXT NOT NULL,
			payload BLOB NOT NULL,
			PRIMARY KEY(workspace_id,node_id,source_id,snapshot_id)
		);
		CREATE INDEX IF NOT EXISTS idx_linkage_context_evidence_latest
			ON linkage_context_evidence(source_id,trade_date,as_of,received_at);
        CREATE INDEX IF NOT EXISTS idx_linkage_context_evidence_workspace_latest
            ON linkage_context_evidence(workspace_id,node_id,source_id,trade_date DESC,as_of DESC,received_at DESC);
		CREATE TABLE IF NOT EXISTS linkage_attention_revisions (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			revision INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id,node_id)
		);
		CREATE TABLE IF NOT EXISTS linkage_attention_sources (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			source TEXT NOT NULL,
			active INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			intent_id TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id,node_id,symbol,source)
		);
		CREATE TABLE IF NOT EXISTS linkage_attention_tombstones (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			revision INTEGER NOT NULL,
			intent_id TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id,node_id,symbol)
		);
		CREATE TABLE IF NOT EXISTS linkage_attention_intents (
			intent_id TEXT PRIMARY KEY,
			request_hash TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			source TEXT NOT NULL,
			action TEXT NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS linkage_workspace_attention_revisions (
			workspace_id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS linkage_workspace_attention_sources (
			workspace_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			source TEXT NOT NULL,
			active INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			intent_id TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id,symbol,source)
		);
		CREATE TABLE IF NOT EXISTS linkage_workspace_attention_tombstones (
			workspace_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			revision INTEGER NOT NULL,
			intent_id TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id,symbol)
		);
		CREATE TABLE IF NOT EXISTS linkage_workspace_attention_migrations (
			workspace_id TEXT PRIMARY KEY,
			migrated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS linkage_targets (
			node_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL,
			key BLOB NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS linkage_auth_nonces (
			nonce_key TEXT PRIMARY KEY,
			timestamp INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS linkage_auth_nonce_time ON linkage_auth_nonces(timestamp);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize linkage database: %w", err)
	}
	if err := migrateLegacyWorkspaceAttention(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate workspace attention: %w", err)
	}
	targets, err := loadTargets(targetsPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := loadPersistedTargets(db, targets); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load registered linkage targets: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		db: db, targets: targets, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
		client: &http.Client{
			Timeout:       4 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{
				Timeout: 2 * time.Second, KeepAlive: 30 * time.Second,
			}).DialContext},
		},
	}
	service.wg.Add(1)
	go service.worker()
	return service, nil
}

func loadTargets(path string) (map[string]Target, error) {
	targets := make(map[string]Target)
	if strings.TrimSpace(path) == "" {
		return targets, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return targets, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read linkage target registry: %w", err)
	}
	var registry targetRegistry
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return nil, fmt.Errorf("decode linkage target registry: %w", err)
	}
	if len(registry.Targets) > 32 {
		return nil, errors.New("linkage target registry exceeds 32 nodes")
	}
	for _, target := range registry.Targets {
		if !target.Enabled {
			continue
		}
		if !nodePattern.MatchString(target.NodeID) || !nodePattern.MatchString(target.WorkspaceID) {
			return nil, fmt.Errorf("invalid linkage identity for node %q", target.NodeID)
		}
		if _, exists := targets[target.NodeID]; exists {
			return nil, fmt.Errorf("duplicate linkage node id %q", target.NodeID)
		}
		parsed, err := url.Parse(target.BaseURL)
		if err != nil || parsed == nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("invalid base_url for linkage node %q", target.NodeID)
		}
		ip := net.ParseIP(parsed.Hostname())
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || ip == nil || ip.To4() == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			return nil, fmt.Errorf("linkage node %q must use a private IPv4 base_url", target.NodeID)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			return nil, fmt.Errorf("linkage node %q base_url cannot include a path prefix", target.NodeID)
		}
		if parsed.Port() == "" {
			return nil, fmt.Errorf("linkage node %q base_url must include a port", target.NodeID)
		}
		keyPath := target.KeyFile
		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(filepath.Dir(path), keyPath)
		}
		key, err := readKey(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key for linkage node %q: %w", target.NodeID, err)
		}
		target.key, target.base = key, parsed
		target.KeyFile = ""
		targets[target.NodeID] = target
	}
	return targets, nil
}

func readKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("key_file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) >= 64 && len(trimmed)%2 == 0 {
		if decoded, err := hex.DecodeString(trimmed); err == nil {
			raw = decoded
		}
	}
	if len(raw) < 32 {
		return nil, errors.New("key must contain at least 32 bytes")
	}
	return raw, nil
}

func (s *Service) Targets() []map[string]string {
	s.targetMu.RLock()
	targets := make(map[string]Target, len(s.targets))
	for nodeID, target := range s.targets {
		targets[nodeID] = target
	}
	s.targetMu.RUnlock()
	result := make([]map[string]string, 0, len(targets))
	ids := make([]string, 0, len(targets))
	for nodeID := range targets {
		ids = append(ids, nodeID)
	}
	sort.Strings(ids)
	for _, nodeID := range ids {
		target := targets[nodeID]
		result = append(result, map[string]string{
			"node_id": target.NodeID, "workspace_id": target.WorkspaceID,
			"label": target.Label, "status": "configured",
		})
	}
	return result
}

func (s *Service) IsConfiguredTarget(workspaceID, nodeID string) bool {
	target, ok := s.target(nodeID)
	return ok && target.Enabled && target.WorkspaceID == workspaceID
}

func (s *Service) Submit(ctx context.Context, command Command) (Operation, bool, error) {
	targets := s.targetSnapshot()
	if err := validateCommand(command, targets); err != nil {
		return Operation{}, false, err
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		return Operation{}, false, err
	}
	hash := sha256.Sum256(canonical)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	var existingID, existingRequest, existingHash string
	err = tx.QueryRowContext(ctx,
		`SELECT operation_id,request_id,command_hash FROM linkage_operations WHERE operation_id=? OR request_id=?`,
		command.OperationID, command.RequestID,
	).Scan(&existingID, &existingRequest, &existingHash)
	if err == nil {
		if existingID != command.OperationID || existingRequest != command.RequestID || existingHash != hex.EncodeToString(hash[:]) {
			return Operation{}, false, errors.New("operation_id_conflict")
		}
		if err := tx.Commit(); err != nil {
			return Operation{}, false, err
		}
		operation, err := s.GetOperation(ctx, command.OperationID)
		return operation, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM linkage_deliveries WHERE status IN ('pending','accepted')`,
	).Scan(&pending); err != nil {
		return Operation{}, false, err
	}
	if pending >= 128 {
		return Operation{}, false, errors.New("linkage_queue_full")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO linkage_operations(operation_id,request_id,command_hash,command_json,created_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		command.OperationID, command.RequestID, hex.EncodeToString(hash[:]), string(canonical), createdAt(command.CreatedAt), command.ExpiresAt, created,
	); err != nil {
		return Operation{}, false, err
	}
	for _, nodeID := range command.TargetNodeIDs {
		target := targets[nodeID]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO linkage_deliveries(operation_id,node_id,workspace_id,status,next_attempt,updated_at) VALUES(?,?,?,'pending',0,?)`,
			command.OperationID, target.NodeID, target.WorkspaceID, created,
		); err != nil {
			return Operation{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, false, err
	}
	s.signalWorker()
	operation, err := s.GetOperation(ctx, command.OperationID)
	return operation, false, err
}

func createdAt(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func validateCommand(command Command, targets map[string]Target) error {
	if command.SchemaVersion != 1 || command.Type != "WATCHLIST_ADD" || command.InstrumentType != "stock" {
		return errors.New("unsupported_command")
	}
	if !idPattern.MatchString(command.RequestID) || !idPattern.MatchString(command.OperationID) {
		return errors.New("invalid_request_id")
	}
	if !nodePattern.MatchString(command.WorkspaceID) || len(command.TargetNodeIDs) == 0 || len(command.TargetNodeIDs) > 10 {
		return errors.New("invalid_target")
	}
	match := symbolPattern.FindStringSubmatch(strings.ToUpper(command.Symbol))
	if match == nil || (match[2] == "SH" && !strings.HasPrefix(match[1], "600") && !strings.HasPrefix(match[1], "601") && !strings.HasPrefix(match[1], "603") && !strings.HasPrefix(match[1], "605") && !strings.HasPrefix(match[1], "688") && !strings.HasPrefix(match[1], "689")) ||
		(match[2] == "SZ" && !strings.HasPrefix(match[1], "000") && !strings.HasPrefix(match[1], "001") && !strings.HasPrefix(match[1], "002") && !strings.HasPrefix(match[1], "003") && !strings.HasPrefix(match[1], "300") && !strings.HasPrefix(match[1], "301")) {
		return errors.New("unsupported_symbol")
	}
	created, err := time.Parse(time.RFC3339, command.CreatedAt)
	if err != nil {
		return errors.New("invalid_created_at")
	}
	expires, err := time.Parse(time.RFC3339, command.ExpiresAt)
	if err != nil {
		return errors.New("invalid_expires_at")
	}
	now := time.Now()
	if created.After(now.Add(time.Minute)) || !expires.After(now) || !expires.After(created) || expires.Sub(created) > maxCommandAge {
		return errors.New("command_expired")
	}
	if len(command.Source) > 80 || !strings.HasPrefix(command.Source, "stock-strategy.") || len(command.Reason) > 280 || len(command.ContextRefs) > 10 {
		return errors.New("invalid_context")
	}
	for _, ref := range command.ContextRefs {
		if len(ref) > 256 {
			return errors.New("invalid_context_refs")
		}
	}
	if command.ObservedTradeDate != "" {
		if _, err := time.Parse("2006-01-02", command.ObservedTradeDate); err != nil {
			return errors.New("invalid_observed_trade_date")
		}
	}
	seen := make(map[string]struct{}, len(command.TargetNodeIDs))
	for _, nodeID := range command.TargetNodeIDs {
		target, ok := targets[nodeID]
		if !ok || target.WorkspaceID != command.WorkspaceID {
			return errors.New("wrong_target")
		}
		if _, exists := seen[nodeID]; exists {
			return errors.New("duplicate_target")
		}
		seen[nodeID] = struct{}{}
	}
	return nil
}

func (s *Service) GetOperation(ctx context.Context, operationID string) (Operation, error) {
	var operation Operation
	err := s.db.QueryRowContext(ctx,
		`SELECT operation_id,request_id,created_at,updated_at FROM linkage_operations WHERE operation_id=?`,
		operationID,
	).Scan(&operation.OperationID, &operation.RequestID, &operation.CreatedAt, &operation.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, sql.ErrNoRows
	}
	if err != nil {
		return Operation{}, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id,workspace_id,status,result_json,error,updated_at FROM linkage_deliveries WHERE operation_id=? ORDER BY node_id`,
		operationID,
	)
	if err != nil {
		return Operation{}, err
	}
	defer rows.Close()
	operation.Status = "applied"
	for rows.Next() {
		var delivery Delivery
		var result string
		if err := rows.Scan(&delivery.NodeID, &delivery.WorkspaceID, &delivery.Status, &result, &delivery.Error, &delivery.UpdatedAt); err != nil {
			return Operation{}, err
		}
		delivery.Result = json.RawMessage(result)
		operation.Deliveries = append(operation.Deliveries, delivery)
		if delivery.Status == "pending" || delivery.Status == "accepted" {
			operation.Status = "accepted"
		} else if delivery.Status == "rejected" || delivery.Status == "expired" || delivery.Status == "failed" {
			if operation.Status != "accepted" {
				operation.Status = "partial"
			}
		}
	}
	return operation, rows.Err()
}

func (s *Service) signalWorker() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		s.processDue()
	}
}

func (s *Service) processDue() {
	rows, err := s.db.QueryContext(s.ctx,
		`SELECT d.operation_id,d.node_id,d.status,d.attempts,o.command_json,o.expires_at
		 FROM linkage_deliveries d JOIN linkage_operations o USING(operation_id)
		 WHERE (d.status IN ('pending','accepted') OR
		        (d.status IN ('applied','already_present') AND d.next_attempt>0))
		   AND d.next_attempt<=? ORDER BY o.created_at LIMIT 8`,
		time.Now().Unix(),
	)
	if err != nil {
		return
	}
	type due struct {
		operationID string
		nodeID      string
		status      string
		attempts    int
		commandJSON string
		expiresAt   string
	}
	var items []due
	for rows.Next() {
		var item due
		if rows.Scan(&item.operationID, &item.nodeID, &item.status, &item.attempts, &item.commandJSON, &item.expiresAt) == nil {
			items = append(items, item)
		}
	}
	_ = rows.Close()
	for _, item := range items {
		if s.ctx.Err() != nil {
			return
		}
		s.deliver(item.operationID, item.nodeID, item.status, item.attempts, item.commandJSON, item.expiresAt)
	}
}

func (s *Service) deliver(operationID, nodeID, status string, attempts int, commandJSON, expiresAt string) {
	target, ok := s.target(nodeID)
	if !ok {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, "target_not_configured", attempts+1, 0)
			return
		}
		s.updateDelivery(operationID, nodeID, "failed", `{}`, "target_not_configured", attempts+1, retryAt(attempts))
		return
	}
	var command Command
	if err := json.Unmarshal([]byte(commandJSON), &command); err != nil {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, "stored_command_invalid", attempts+1, 0)
			return
		}
		s.updateDelivery(operationID, nodeID, "failed", `{}`, "stored_command_invalid", attempts+1, retryAt(attempts))
		return
	}
	if status == "pending" {
		expires, err := time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			s.updateDelivery(operationID, nodeID, "expired", `{}`, "command_expired_before_delivery", attempts+1, 0)
			return
		}
		if !expires.After(time.Now()) {
			s.pollOperation(operationID, nodeID, status, attempts, expiresAt)
			return
		}
		command.TargetNodeIDs = []string{target.NodeID}
		command.WorkspaceID = target.WorkspaceID
		body, err := json.Marshal(command)
		if err != nil {
			s.updateDelivery(operationID, nodeID, "failed", `{}`, "command_encode_failed", attempts+1, retryAt(attempts))
			return
		}
		result, statusCode, err := s.request(target, http.MethodPost, "/bridge/v1/commands", body)
		if err != nil {
			s.updateDelivery(operationID, nodeID, "pending", `{}`, safeError(err), attempts+1, retryAt(attempts))
			return
		}
		if statusCode == http.StatusAccepted || statusCode == http.StatusOK {
			s.updateDelivery(operationID, nodeID, "accepted", string(result), "", attempts+1, time.Now().Add(deliveryPollInterval).Unix())
			return
		}
		nextStatus := "failed"
		if statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError {
			nextStatus = "pending"
		}
		s.updateDelivery(operationID, nodeID, nextStatus, string(result), fmt.Sprintf("desktop_http_%d", statusCode), attempts+1, retryAt(attempts))
		return
	}
	s.pollOperation(operationID, nodeID, status, attempts, expiresAt)
}

func (s *Service) pollOperation(operationID, nodeID, status string, attempts int, expiresAt string) {
	target, ok := s.target(nodeID)
	if !ok {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, "target_not_configured", attempts+1, 0)
			return
		}
		s.updateDelivery(operationID, nodeID, "failed", `{}`, "target_not_configured", attempts+1, retryAt(attempts))
		return
	}
	result, statusCode, err := s.request(target, http.MethodGet, "/bridge/v1/operations/"+url.PathEscape(operationID), nil)
	if err != nil {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, safeError(err), attempts+1, consumerRetryAt(expiresAt))
			return
		}
		s.updateDelivery(operationID, nodeID, "accepted", `{}`, safeError(err), attempts+1, retryAt(attempts))
		return
	}
	if statusCode == http.StatusNotFound {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, "desktop_operation_not_found", attempts+1, consumerRetryAt(expiresAt))
			return
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
		if parseErr != nil || !expires.After(time.Now()) {
			s.updateDelivery(operationID, nodeID, "expired", `{}`, "command_expired_before_delivery", attempts+1, 0)
			return
		}
		s.updateDelivery(operationID, nodeID, "pending", `{}`, "desktop_operation_not_found", attempts+1, retryAt(attempts))
		return
	}
	if statusCode != http.StatusOK {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, fmt.Sprintf("desktop_http_%d", statusCode), attempts+1, consumerRetryAt(expiresAt))
			return
		}
		s.updateDelivery(operationID, nodeID, "accepted", string(result), fmt.Sprintf("desktop_http_%d", statusCode), attempts+1, retryAt(attempts))
		return
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		if successfulDeliveryStatus(status) {
			s.retryDeliveryPreservingResult(operationID, nodeID, "desktop_response_invalid", attempts+1, consumerRetryAt(expiresAt))
			return
		}
		s.updateDelivery(operationID, nodeID, "accepted", string(result), "desktop_response_invalid", attempts+1, retryAt(attempts))
		return
	}
	nextStatus := desktopDeliveryStatus(response.Status)
	if successfulDeliveryStatus(status) && !successfulDeliveryStatus(nextStatus) {
		s.retryDeliveryPreservingResult(operationID, nodeID, "desktop_operation_not_terminal", attempts+1, consumerRetryAt(expiresAt))
		return
	}
	nextAttempt := int64(0)
	if nextStatus == "accepted" {
		nextAttempt = time.Now().Add(deliveryPollInterval).Unix()
	} else if nextStatus == "applied" || nextStatus == "already_present" {
		if consumerReconciliationNeeded(result) {
			nextAttempt = consumerRetryAt(expiresAt)
		}
	}
	s.updateDelivery(operationID, nodeID, nextStatus, string(result), "", attempts+1, nextAttempt)
}

func successfulDeliveryStatus(status string) bool {
	return status == "applied" || status == "already_present"
}

func consumerRetryAt(expiresAt string) int64 {
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return 0
	}
	next := time.Now().Add(deliveryPollInterval)
	if !expires.After(next) {
		// Command expiry forbids a new effect, not reconciliation of a saved one.
		return time.Now().Add(30 * time.Second).Unix()
	}
	return next.Unix()
}

func consumerReconciliationNeeded(raw []byte) bool {
	var response struct {
		Result struct {
			Consumers map[string]struct {
				Status      string `json:"status"`
				QuoteStatus string `json:"quote_status"`
			} `json:"consumers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Result.Consumers == nil {
		return false
	}
	for _, consumer := range []string{"TK", "ATS"} {
		status, ok := response.Result.Consumers[consumer]
		if !ok || (status.Status != "synced" && status.Status != "error") ||
			(status.Status == "synced" && status.QuoteStatus != "ready" && status.QuoteStatus != "market_closed") {
			return true
		}
	}
	return false
}

func desktopDeliveryStatus(status string) string {
	switch status {
	case "applied", "already_present", "rejected", "expired", "failed":
		return status
	default:
		return "accepted"
	}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func retryAt(attempts int) int64 {
	seconds := 1 << min(attempts, 5)
	if seconds > 30 {
		seconds = 30
	}
	return time.Now().Add(time.Duration(seconds) * time.Second).Unix()
}

func (s *Service) updateDelivery(operationID, nodeID, status, result, message string, attempts int, nextAttempt int64) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(s.ctx,
		`UPDATE linkage_deliveries SET status=?,result_json=?,error=?,attempts=?,next_attempt=?,updated_at=? WHERE operation_id=? AND node_id=?`,
		status, result, message, attempts, nextAttempt, now, operationID, nodeID,
	)
	_, _ = s.db.ExecContext(s.ctx,
		`UPDATE linkage_operations SET updated_at=? WHERE operation_id=?`, now, operationID,
	)
}

func (s *Service) retryDeliveryPreservingResult(operationID, nodeID, message string, attempts int, nextAttempt int64) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(s.ctx,
		`UPDATE linkage_deliveries SET error=?,attempts=?,next_attempt=?,updated_at=? WHERE operation_id=? AND node_id=?`,
		message, attempts, nextAttempt, now, operationID, nodeID,
	)
	_, _ = s.db.ExecContext(s.ctx,
		`UPDATE linkage_operations SET updated_at=? WHERE operation_id=?`, now, operationID,
	)
}

func (s *Service) request(target Target, method, path string, body []byte) ([]byte, int, error) {
	return s.requestContext(s.ctx, target, method, path, body)
}

func (s *Service) requestContext(ctx context.Context, target Target, method, path string, body []byte) ([]byte, int, error) {
	endpoint := *target.base
	endpoint.Path = path
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, 0, err
	}
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	var nonceBytes [24]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return nil, 0, err
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{method, path, hex.EncodeToString(bodyHash[:]), timestamp, nonce, target.NodeID, target.WorkspaceID}, "\n")
	mac := hmac.New(sha256.New, target.key)
	_, _ = mac.Write([]byte(canonical))
	req.Header.Set("X-Linkage-Node-ID", target.NodeID)
	req.Header.Set("X-Linkage-Workspace-ID", target.WorkspaceID)
	req.Header.Set("X-Linkage-Timestamp", timestamp)
	req.Header.Set("X-Linkage-Nonce", nonce)
	req.Header.Set("X-Linkage-Signature", hex.EncodeToString(mac.Sum(nil)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return data, response.StatusCode, err
}

func (s *Service) HasWorkspace(workspaceID string) bool {
	if strings.TrimSpace(workspaceID) == "" {
		return false
	}
	s.targetMu.RLock()
	defer s.targetMu.RUnlock()
	for _, target := range s.targets {
		if target.WorkspaceID == workspaceID {
			return true
		}
	}
	return false
}

func (s *Service) LatestContextSnapshotForWorkspace(ctx context.Context, workspaceID string) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM linkage_context_snapshots
		 WHERE CASE WHEN json_valid(CAST(payload AS TEXT)) THEN json_extract(CAST(payload AS TEXT),'$.workspace_id') END=?
		 ORDER BY created_at DESC,snapshot_id DESC LIMIT 1`, workspaceID,
	).Scan(&payload)
	return payload, err
}

func (s *Service) LatestContextSnapshot(ctx context.Context) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM linkage_context_snapshots ORDER BY created_at DESC LIMIT 1`,
	).Scan(&payload)
	return payload, err
}

func (s *Service) StoreContextEvidence(ctx context.Context, evidence ContextEvidence) (bool, error) {
	if evidence.WorkspaceID == "" || evidence.NodeID == "" || evidence.SourceID == "" ||
		evidence.SnapshotID == "" || evidence.TradeDate == "" || evidence.AsOf == "" ||
		len(evidence.Payload) == 0 || len(evidence.Payload) > 512<<10 {
		return false, errors.New("invalid_context_evidence")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO linkage_context_evidence(workspace_id,node_id,source_id,snapshot_id,trade_date,as_of,received_at,payload) VALUES(?,?,?,?,?,?,?,?)`,
		evidence.WorkspaceID, evidence.NodeID, evidence.SourceID, evidence.SnapshotID, evidence.TradeDate, evidence.AsOf, now, []byte(evidence.Payload),
	)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM linkage_context_evidence WHERE workspace_id=? AND node_id=? AND source_id=? AND snapshot_id NOT IN (
			SELECT snapshot_id FROM linkage_context_evidence WHERE workspace_id=? AND node_id=? AND source_id=?
			ORDER BY trade_date DESC,as_of DESC,received_at DESC LIMIT 500
		)`,
		evidence.WorkspaceID, evidence.NodeID, evidence.SourceID, evidence.WorkspaceID, evidence.NodeID, evidence.SourceID,
	); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM linkage_context_evidence WHERE received_at < ?`, time.Now().UTC().Add(-90*24*time.Hour).Format(time.RFC3339Nano),
	); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return inserted > 0, nil
}

func (s *Service) LatestContextEvidence(ctx context.Context) ([]ContextEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT current.workspace_id,current.node_id,current.source_id,current.snapshot_id,
		current.trade_date,current.as_of,current.received_at,current.payload
		FROM linkage_context_evidence AS current
		WHERE current.snapshot_id=(SELECT latest.snapshot_id FROM linkage_context_evidence AS latest
			WHERE latest.workspace_id=current.workspace_id AND latest.node_id=current.node_id AND latest.source_id=current.source_id
			ORDER BY latest.trade_date DESC,latest.as_of DESC,latest.received_at DESC LIMIT 1)
		ORDER BY current.source_id,current.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := make([]ContextEvidence, 0)
	for rows.Next() {
		var item ContextEvidence
		var payload []byte
		if err := rows.Scan(&item.WorkspaceID, &item.NodeID, &item.SourceID, &item.SnapshotID,
			&item.TradeDate, &item.AsOf, &item.ReceivedAt, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		evidence = append(evidence, item)
	}
	return evidence, rows.Err()
}

func (s *Service) RecentContextEvidence(ctx context.Context) ([]ContextEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT current.workspace_id,current.node_id,current.source_id,current.snapshot_id,
		current.trade_date,current.as_of,current.received_at,current.payload
		FROM linkage_context_evidence AS current
		WHERE current.snapshot_id IN (
			SELECT recent.snapshot_id FROM linkage_context_evidence AS recent
			WHERE recent.workspace_id=current.workspace_id AND recent.node_id=current.node_id AND recent.source_id=current.source_id
			ORDER BY recent.trade_date DESC,recent.as_of DESC,recent.received_at DESC LIMIT 500
		)
		ORDER BY current.source_id,current.node_id,current.trade_date,current.as_of,current.received_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := make([]ContextEvidence, 0)
	for rows.Next() {
		var item ContextEvidence
		var payload []byte
		if err := rows.Scan(&item.WorkspaceID, &item.NodeID, &item.SourceID, &item.SnapshotID,
			&item.TradeDate, &item.AsOf, &item.ReceivedAt, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		evidence = append(evidence, item)
	}
	return evidence, rows.Err()
}

func (s *Service) RecentContextEvidenceForWorkspace(ctx context.Context, workspaceID string) ([]ContextEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT current.workspace_id,current.node_id,current.source_id,current.snapshot_id,
		current.trade_date,current.as_of,current.received_at,current.payload
		FROM linkage_context_evidence AS current
		WHERE current.workspace_id=? AND current.snapshot_id IN (
			SELECT recent.snapshot_id FROM linkage_context_evidence AS recent
			WHERE recent.workspace_id=current.workspace_id AND recent.node_id=current.node_id AND recent.source_id=current.source_id
			ORDER BY recent.trade_date DESC,recent.as_of DESC,recent.received_at DESC LIMIT 500
		)
		ORDER BY current.source_id,current.node_id,current.trade_date,current.as_of,current.received_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := make([]ContextEvidence, 0)
	for rows.Next() {
		var item ContextEvidence
		var payload []byte
		if err := rows.Scan(&item.WorkspaceID, &item.NodeID, &item.SourceID, &item.SnapshotID,
			&item.TradeDate, &item.AsOf, &item.ReceivedAt, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		evidence = append(evidence, item)
	}
	return evidence, rows.Err()
}

func (s *Service) StoreContextSnapshot(ctx context.Context, snapshotID, tradeDate string, payload []byte, events []ContextEvent) error {
	if snapshotID == "" || len(payload) == 0 || len(payload) > 8<<20 {
		return errors.New("invalid_context_snapshot")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO linkage_context_snapshots(snapshot_id,trade_date,created_at,payload) VALUES(?,?,?,?)`,
		snapshotID, tradeDate, now, payload,
	); err != nil {
		return err
	}
	for _, event := range events {
		sourceIDs, marshalErr := json.Marshal(event.SourceSnapshotIDs)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO linkage_context_events(event_id,event_type,origin_node,causation_id,source_snapshot_ids,trade_date,occurred_at,payload) VALUES(?,?,?,?,?,?,?,?)`,
			event.EventID, event.EventType, event.OriginNode, event.CausationID, string(sourceIDs), event.TradeDate, event.OccurredAt, []byte(event.Payload),
		); err != nil {
			return err
		}
	}
	// Expire events before snapshots; retained events keep their replay dependencies.
	_, err = tx.ExecContext(ctx,
		`DELETE FROM linkage_context_events WHERE occurred_at < ?`, time.Now().UTC().Add(-90*24*time.Hour).Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM linkage_context_snapshots
		WHERE (created_at < ? OR snapshot_id NOT IN (
			SELECT snapshot_id FROM linkage_context_snapshots ORDER BY created_at DESC,snapshot_id DESC LIMIT 1000
		)) AND snapshot_id NOT IN (
            SELECT causation_id FROM linkage_context_events
            UNION
            SELECT CAST(reference.value AS TEXT) FROM linkage_context_events AS event,
                json_each(event.source_snapshot_ids) AS reference WHERE reference.type='text'
        )`, time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ContextEvents(ctx context.Context, after string, limit int) ([]ContextEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := `SELECT event_id,event_type,origin_node,causation_id,source_snapshot_ids,trade_date,occurred_at,payload
		FROM linkage_context_events ORDER BY occurred_at,event_id LIMIT ?`
	args := []any{limit}
	if after != "" {
		var cursorTime string
		err := s.db.QueryRowContext(ctx, `SELECT occurred_at FROM linkage_context_events WHERE event_id=?`, after).Scan(&cursorTime)
		if err == nil {
			query = `SELECT event_id,event_type,origin_node,causation_id,source_snapshot_ids,trade_date,occurred_at,payload
				FROM linkage_context_events WHERE occurred_at>? OR (occurred_at=? AND event_id>?) ORDER BY occurred_at,event_id LIMIT ?`
			args = []any{cursorTime, cursorTime, after, limit}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]ContextEvent, 0, limit)
	for rows.Next() {
		var event ContextEvent
		var sources string
		if err := rows.Scan(&event.EventID, &event.EventType, &event.OriginNode, &event.CausationID, &sources, &event.TradeDate, &event.OccurredAt, &event.Payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &event.SourceSnapshotIDs); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// Resolve workspace snapshot IDs once using the expression index, then seek events.
// A correlated JSON lookup per event can block the single-writer connection for seconds.
const contextEventWorkspaceScope = `event.causation_id IN (
 SELECT snapshot_id FROM linkage_context_snapshots
 WHERE CASE WHEN json_valid(CAST(payload AS TEXT)) THEN json_extract(CAST(payload AS TEXT),'$.workspace_id') END=?)`

func (s *Service) ContextEventsForWorkspace(ctx context.Context, workspaceID, after string, limit int) ([]ContextEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	scope := contextEventWorkspaceScope
	query := `SELECT event.event_id,event.event_type,event.origin_node,event.causation_id,event.source_snapshot_ids,
		event.trade_date,event.occurred_at,event.payload FROM linkage_context_events AS event WHERE ` + scope +
		` ORDER BY event.occurred_at,event.event_id LIMIT ?`
	args := []any{workspaceID, limit}
	if after != "" {
		var cursorTime string
		err := s.db.QueryRowContext(ctx, `SELECT event.occurred_at FROM linkage_context_events AS event
			WHERE event.event_id=? AND `+scope, after, workspaceID).Scan(&cursorTime)
		if err == nil {
			query = `SELECT event.event_id,event.event_type,event.origin_node,event.causation_id,event.source_snapshot_ids,
				event.trade_date,event.occurred_at,event.payload FROM linkage_context_events AS event
				WHERE ` + scope + ` AND (event.occurred_at>? OR (event.occurred_at=? AND event.event_id>?))
				ORDER BY event.occurred_at,event.event_id LIMIT ?`
			args = []any{workspaceID, cursorTime, cursorTime, after, limit}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContextEvents(rows, limit)
}

func (s *Service) LatestContextEventsForWorkspace(ctx context.Context, workspaceID string, limit int) ([]ContextEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event.event_id,event.event_type,event.origin_node,event.causation_id,
		event.source_snapshot_ids,event.trade_date,event.occurred_at,event.payload
		FROM linkage_context_events AS event
		WHERE `+contextEventWorkspaceScope+`
		ORDER BY event.occurred_at DESC,event.event_id DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := scanContextEvents(rows, limit)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}

func scanContextEvents(rows *sql.Rows, limit int) ([]ContextEvent, error) {
	events := make([]ContextEvent, 0, limit)
	for rows.Next() {
		var event ContextEvent
		var sources string
		if err := rows.Scan(&event.EventID, &event.EventType, &event.OriginNode, &event.CausationID,
			&sources, &event.TradeDate, &event.OccurredAt, &event.Payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &event.SourceSnapshotIDs); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Service) LatestContextEvents(ctx context.Context, limit int) ([]ContextEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,event_type,origin_node,causation_id,source_snapshot_ids,trade_date,occurred_at,payload
		FROM linkage_context_events ORDER BY occurred_at DESC,event_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]ContextEvent, 0, limit)
	for rows.Next() {
		var event ContextEvent
		var sources string
		if err := rows.Scan(&event.EventID, &event.EventType, &event.OriginNode, &event.CausationID,
			&sources, &event.TradeDate, &event.OccurredAt, &event.Payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &event.SourceSnapshotIDs); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}

func (s *Service) Close() error {
	s.cancel()
	s.wg.Wait()
	return s.db.Close()
}
