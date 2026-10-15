package linkage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var setupKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

type SetupPackage struct {
	SchemaVersion  int    `json:"schema_version"`
	Kind           string `json:"kind"`
	NodeID         string `json:"node_id"`
	WorkspaceID    string `json:"workspace_id"`
	Label          string `json:"label"`
	Address        string `json:"address"`
	Port           int    `json:"port"`
	HMACKey        string `json:"hmac_key"`
	BackendBaseURL string `json:"backend_base_url"`
}

type SetupResult struct {
	NodeID           string `json:"node_id"`
	WorkspaceID      string `json:"workspace_id"`
	TargetRegistered bool   `json:"target_registered"`
	DesktopStatus    string `json:"desktop_status"`
	DesktopError     string `json:"desktop_error,omitempty"`
}

func (s *Service) target(nodeID string) (Target, bool) {
	s.targetMu.RLock()
	target, ok := s.targets[nodeID]
	s.targetMu.RUnlock()
	return target, ok
}

func (s *Service) targetSnapshot() map[string]Target {
	s.targetMu.RLock()
	targets := make(map[string]Target, len(s.targets))
	for nodeID, target := range s.targets {
		targets[nodeID] = target
	}
	s.targetMu.RUnlock()
	return targets
}

func loadPersistedTargets(db *sql.DB, targets map[string]Target) error {
	rows, err := db.Query(`SELECT node_id,workspace_id,label,base_url,key,enabled FROM linkage_targets ORDER BY node_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var target Target
		var key []byte
		var enabled int
		if err := rows.Scan(&target.NodeID, &target.WorkspaceID, &target.Label, &target.BaseURL, &key, &enabled); err != nil {
			return err
		}
		parsed, err := validatePrivateBaseURL(target.BaseURL, false)
		if err != nil || len(key) < 32 || !nodePattern.MatchString(target.NodeID) || !nodePattern.MatchString(target.WorkspaceID) {
			return fmt.Errorf("invalid persisted target %q", target.NodeID)
		}
		target.key, target.base, target.Enabled = append([]byte(nil), key...), parsed, enabled != 0
		if existing, ok := targets[target.NodeID]; ok && existing.WorkspaceID != target.WorkspaceID {
			return fmt.Errorf("persisted target %q conflicts with static registry workspace", target.NodeID)
		}
		if _, exists := targets[target.NodeID]; !exists && len(targets) >= 32 {
			return errors.New("persisted linkage target registry exceeds 32 nodes")
		}
		targets[target.NodeID] = target
	}
	return rows.Err()
}

func validatePrivateBaseURL(raw string, requirePort bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid private base URL")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || ip.To4() == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return nil, errors.New("base URL must use a private IPv4 address")
	}
	if requirePort && parsed.Port() == "" {
		return nil, errors.New("base URL must include a port")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func (s *Service) ImportSetup(ctx context.Context, setup SetupPackage) (SetupResult, error) {
	s.targetWriteMu.Lock()
	defer s.targetWriteMu.Unlock()
	if setup.SchemaVersion != 1 || setup.Kind != "stock-standalone.system-linkage" ||
		!nodePattern.MatchString(setup.NodeID) || !nodePattern.MatchString(setup.WorkspaceID) ||
		setup.Port < 1 || setup.Port > 65535 || !setupKeyPattern.MatchString(setup.HMACKey) || len(setup.HMACKey)%2 != 0 {
		return SetupResult{}, errors.New("invalid_linkage_setup_package")
	}
	key, err := hex.DecodeString(setup.HMACKey)
	if err != nil || len(key) < 32 {
		return SetupResult{}, errors.New("invalid_linkage_setup_key")
	}
	address := net.ParseIP(strings.TrimSpace(setup.Address))
	if address == nil || address.To4() == nil || (!address.IsPrivate() && !address.IsLoopback()) {
		return SetupResult{}, errors.New("linkage_address_must_be_private_ipv4")
	}
	base, err := validatePrivateBaseURL(setup.BackendBaseURL, false)
	if err != nil {
		return SetupResult{}, errors.New("backend_base_url_must_be_private_ipv4")
	}
	desktopBase := &url.URL{Scheme: "http", Host: net.JoinHostPort(address.String(), fmt.Sprint(setup.Port))}
	target := Target{
		NodeID: setup.NodeID, WorkspaceID: setup.WorkspaceID, Label: strings.TrimSpace(setup.Label),
		BaseURL: desktopBase.String(), Enabled: true, key: key, base: desktopBase,
	}
	if len(target.Label) > 96 {
		target.Label = target.Label[:96]
	}

	s.targetMu.RLock()
	existing, exists := s.targets[target.NodeID]
	s.targetMu.RUnlock()
	if exists && existing.WorkspaceID != target.WorkspaceID {
		return SetupResult{}, errors.New("linkage_node_workspace_conflict")
	}
	if !exists && len(s.targetSnapshot()) >= 32 {
		return SetupResult{}, errors.New("linkage_target_limit_reached")
	}
	writeResult, err := s.db.ExecContext(ctx, `INSERT INTO linkage_targets(node_id,workspace_id,label,base_url,key,enabled,updated_at)
		VALUES(?,?,?,?,?,1,?) ON CONFLICT(node_id) DO UPDATE SET label=excluded.label,base_url=excluded.base_url,
		key=excluded.key,enabled=1,updated_at=excluded.updated_at
		WHERE linkage_targets.workspace_id=excluded.workspace_id`,
		target.NodeID, target.WorkspaceID, target.Label, target.BaseURL, target.key, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return SetupResult{}, err
	}
	if affected, err := writeResult.RowsAffected(); err != nil || affected != 1 {
		return SetupResult{}, errors.New("linkage_node_workspace_conflict")
	}
	s.targetMu.Lock()
	s.targets[target.NodeID] = target
	s.targetMu.Unlock()

	configureBody, _ := json.Marshal(map[string]any{
		"schema_version": 1, "stock_strategy_base_url": strings.TrimRight(base.String(), "/"),
	})
	result := SetupResult{NodeID: target.NodeID, WorkspaceID: target.WorkspaceID,
		TargetRegistered: true, DesktopStatus: "pending"}
	responseBody, status, requestErr := s.requestContext(ctx, target, http.MethodPost, "/bridge/v1/configure", configureBody)
	if requestErr != nil {
		result.DesktopError = "desktop_unreachable"
		return result, nil
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		result.DesktopError = fmt.Sprintf("desktop_http_%d", status)
		return result, nil
	}
	var configured struct {
		Status         string `json:"status"`
		NodeID         string `json:"node_id"`
		WorkspaceID    string `json:"workspace_id"`
		ConnectionTest string `json:"connection_test"`
	}
	if err := json.Unmarshal(responseBody, &configured); err != nil || configured.Status != "configured" ||
		configured.NodeID != target.NodeID || configured.WorkspaceID != target.WorkspaceID || configured.ConnectionTest != "hmac_verified" {
		result.DesktopError = "desktop_configuration_unconfirmed"
		return result, nil
	}
	result.DesktopStatus = "connected"
	return result, nil
}

func (s *Service) AuthenticatePeerRequest(method, requestURI string, headers http.Header, body []byte) bool {
	nodeID := headers.Get("X-Linkage-Node-ID")
	workspaceID := headers.Get("X-Linkage-Workspace-ID")
	timestampText := headers.Get("X-Linkage-Timestamp")
	nonce := headers.Get("X-Linkage-Nonce")
	signature := headers.Get("X-Linkage-Signature")
	if !nodePattern.MatchString(nodeID) || !nodePattern.MatchString(workspaceID) ||
		!idPattern.MatchString(nonce) || len(nonce) < 16 || len(signature) != 64 {
		return false
	}
	timestampSeconds, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return false
	}
	delta := time.Now().Unix() - timestampSeconds
	if delta > 60 || delta < -60 {
		return false
	}
	target, ok := s.target(nodeID)
	if !ok || !target.Enabled || target.WorkspaceID != workspaceID ||
		!peerRouteAllowed(method, requestURI, body, workspaceID, nodeID) {
		return false
	}
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{method, requestURI, hex.EncodeToString(bodyHash[:]), timestampText,
		nonce, nodeID, workspaceID}, "\n")
	mac := hmac.New(sha256.New, target.key)
	_, _ = mac.Write([]byte(canonical))
	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(mac.Sum(nil), provided) {
		return false
	}
	_, err = s.db.Exec(`DELETE FROM linkage_auth_nonces WHERE timestamp < ?`, time.Now().Unix()-600)
	if err != nil {
		return false
	}
	_, err = s.db.Exec(`INSERT INTO linkage_auth_nonces(nonce_key,timestamp) VALUES(?,?)`,
		nodeID+":"+workspaceID+":"+nonce, timestampSeconds)
	return err == nil
}

func peerRouteAllowed(method, requestURI string, body []byte, workspaceID, nodeID string) bool {
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil {
		return false
	}
	switch {
	case method == http.MethodGet && (parsed.Path == "/api/v1/linkage/context" || parsed.Path == "/api/v1/linkage/events"):
		return true
	case method == http.MethodGet && parsed.Path == "/api/v1/linkage/attention":
		return parsed.Query().Get("workspace_id") == workspaceID && parsed.Query().Get("node_id") == nodeID
	case method == http.MethodPost && (parsed.Path == "/api/v1/linkage/context/evidence" ||
		parsed.Path == "/api/v1/linkage/attention/intents" || parsed.Path == "/api/v1/linkage/attention/intents/batch" ||
		parsed.Path == "/api/v1/linkage/strategy/claim" || parsed.Path == "/api/v1/linkage/strategy/result" || parsed.Path == "/api/v1/linkage/strategy/heartbeat"):
		var identity struct {
			WorkspaceID string `json:"workspace_id"`
			NodeID      string `json:"node_id"`
		}
		return json.Unmarshal(body, &identity) == nil && identity.WorkspaceID == workspaceID && identity.NodeID == nodeID
	default:
		return false
	}
}
