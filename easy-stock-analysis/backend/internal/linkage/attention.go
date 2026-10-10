package linkage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var attentionSourcePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

type AttentionIntent struct {
	IntentID     string `json:"intent_id"`
	WorkspaceID  string `json:"workspace_id"`
	NodeID       string `json:"node_id"`
	Symbol       string `json:"symbol"`
	Source       string `json:"source"`
	Action       string `json:"action"`
	BaseRevision int64  `json:"base_revision"`
}

type AttentionSource struct {
	Symbol    string `json:"symbol"`
	Source    string `json:"source"`
	Active    bool   `json:"active"`
	Revision  int64  `json:"revision"`
	IntentID  string `json:"intent_id"`
	UpdatedAt string `json:"updated_at"`
}

type AttentionTombstone struct {
	Symbol    string `json:"symbol"`
	Revision  int64  `json:"revision"`
	IntentID  string `json:"intent_id"`
	UpdatedAt string `json:"updated_at"`
}

type AttentionSnapshot struct {
	SchemaVersion int                  `json:"schema_version"`
	WorkspaceID   string               `json:"workspace_id"`
	NodeID        string               `json:"node_id"`
	Revision      int64                `json:"revision"`
	Sources       []AttentionSource    `json:"sources"`
	Tombstones    []AttentionTombstone `json:"tombstones"`
	UpdatedAt     string               `json:"updated_at"`
}

type AttentionIntentResult struct {
	IntentID string `json:"intent_id"`
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
	Replayed bool   `json:"replayed"`
}

func (s *Service) ApplyAttentionIntent(ctx context.Context, intent AttentionIntent) (AttentionIntentResult, error) {
	if !idPattern.MatchString(intent.IntentID) || !nodePattern.MatchString(intent.WorkspaceID) || !nodePattern.MatchString(intent.NodeID) {
		return AttentionIntentResult{}, errors.New("invalid_attention_identity")
	}
	if !symbolPattern.MatchString(intent.Symbol) || !attentionSourcePattern.MatchString(intent.Source) {
		return AttentionIntentResult{}, errors.New("invalid_attention_source")
	}
	intent.Action = strings.ToLower(strings.TrimSpace(intent.Action))
	if intent.Action != "add" && intent.Action != "remove_source" && intent.Action != "remove_all" {
		return AttentionIntentResult{}, errors.New("invalid_attention_action")
	}
	if intent.BaseRevision < 0 {
		return AttentionIntentResult{}, errors.New("invalid_attention_revision")
	}
	canonical, err := json.Marshal(struct {
		WorkspaceID  string `json:"workspace_id"`
		NodeID       string `json:"node_id"`
		Symbol       string `json:"symbol"`
		Source       string `json:"source"`
		Action       string `json:"action"`
		BaseRevision int64  `json:"base_revision"`
	}{intent.WorkspaceID, intent.NodeID, intent.Symbol, intent.Source, intent.Action, intent.BaseRevision})
	if err != nil {
		return AttentionIntentResult{}, err
	}
	digest := sha256.Sum256(canonical)
	requestHash := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttentionIntentResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var oldHash string
	var oldRevision int64
	err = tx.QueryRowContext(ctx, `SELECT request_hash,revision FROM linkage_attention_intents WHERE intent_id=?`, intent.IntentID).Scan(&oldHash, &oldRevision)
	if err == nil {
		if oldHash != requestHash {
			return AttentionIntentResult{}, errors.New("attention_intent_conflict")
		}
		return AttentionIntentResult{IntentID: intent.IntentID, Status: "applied", Revision: oldRevision, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AttentionIntentResult{}, err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO linkage_workspace_attention_revisions(workspace_id,revision,updated_at) VALUES(?,0,?)`,
		intent.WorkspaceID, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return AttentionIntentResult{}, err
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE linkage_workspace_attention_revisions SET revision=revision+1,updated_at=? WHERE workspace_id=? AND revision=?`,
		time.Now().UTC().Format(time.RFC3339Nano), intent.WorkspaceID, intent.BaseRevision,
	)
	if err != nil {
		return AttentionIntentResult{}, err
	}
	updated, err := update.RowsAffected()
	if err != nil {
		return AttentionIntentResult{}, err
	}
	if updated != 1 {
		var revision int64
		if err := tx.QueryRowContext(ctx,
			`SELECT revision FROM linkage_workspace_attention_revisions WHERE workspace_id=?`, intent.WorkspaceID,
		).Scan(&revision); err != nil {
			return AttentionIntentResult{}, err
		}
		return AttentionIntentResult{Revision: revision}, errors.New("attention_revision_conflict")
	}
	var revision int64
	if err := tx.QueryRowContext(ctx,
		`SELECT revision FROM linkage_workspace_attention_revisions WHERE workspace_id=?`, intent.WorkspaceID,
	).Scan(&revision); err != nil {
		return AttentionIntentResult{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch intent.Action {
	case "add":
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO linkage_workspace_attention_sources(workspace_id,symbol,source,active,revision,intent_id,updated_at) VALUES(?,?,?,1,?,?,?)
			ON CONFLICT(workspace_id,symbol,source) DO UPDATE SET active=1,revision=excluded.revision,intent_id=excluded.intent_id,updated_at=excluded.updated_at`,
			intent.WorkspaceID, intent.Symbol, intent.Source, revision, intent.IntentID, now,
		); err != nil {
			return AttentionIntentResult{}, err
		}
		if _, err = tx.ExecContext(ctx,
			`DELETE FROM linkage_workspace_attention_tombstones WHERE workspace_id=? AND symbol=?`,
			intent.WorkspaceID, intent.Symbol,
		); err != nil {
			return AttentionIntentResult{}, err
		}
	case "remove_source":
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO linkage_workspace_attention_sources(workspace_id,symbol,source,active,revision,intent_id,updated_at) VALUES(?,?,?,0,?,?,?)
			ON CONFLICT(workspace_id,symbol,source) DO UPDATE SET active=0,revision=excluded.revision,intent_id=excluded.intent_id,updated_at=excluded.updated_at`,
			intent.WorkspaceID, intent.Symbol, intent.Source, revision, intent.IntentID, now,
		); err != nil {
			return AttentionIntentResult{}, err
		}
	case "remove_all":
		if _, err = tx.ExecContext(ctx,
			`UPDATE linkage_workspace_attention_sources SET active=0,revision=?,intent_id=?,updated_at=? WHERE workspace_id=? AND symbol=?`,
			revision, intent.IntentID, now, intent.WorkspaceID, intent.Symbol,
		); err != nil {
			return AttentionIntentResult{}, err
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO linkage_workspace_attention_tombstones(workspace_id,symbol,revision,intent_id,updated_at) VALUES(?,?,?,?,?)
			ON CONFLICT(workspace_id,symbol) DO UPDATE SET revision=excluded.revision,intent_id=excluded.intent_id,updated_at=excluded.updated_at`,
			intent.WorkspaceID, intent.Symbol, revision, intent.IntentID, now,
		); err != nil {
			return AttentionIntentResult{}, err
		}
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO linkage_attention_intents(intent_id,request_hash,workspace_id,node_id,symbol,source,action,revision,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		intent.IntentID, requestHash, intent.WorkspaceID, intent.NodeID, intent.Symbol, intent.Source, intent.Action, revision, now,
	); err != nil {
		return AttentionIntentResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return AttentionIntentResult{}, err
	}
	return AttentionIntentResult{IntentID: intent.IntentID, Status: "applied", Revision: revision}, nil
}

func (s *Service) GetAttentionSnapshot(ctx context.Context, workspaceID, nodeID string) (AttentionSnapshot, error) {
	if !nodePattern.MatchString(workspaceID) || !nodePattern.MatchString(nodeID) {
		return AttentionSnapshot{}, errors.New("invalid_attention_identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttentionSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot := AttentionSnapshot{SchemaVersion: 1, WorkspaceID: workspaceID, NodeID: nodeID,
		Sources: []AttentionSource{}, Tombstones: []AttentionTombstone{}}
	var updatedAt sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT revision,updated_at FROM linkage_workspace_attention_revisions WHERE workspace_id=?`, workspaceID,
	).Scan(&snapshot.Revision, &updatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AttentionSnapshot{}, err
	}
	if updatedAt.Valid {
		snapshot.UpdatedAt = updatedAt.String
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT symbol,source,active,revision,intent_id,updated_at FROM linkage_workspace_attention_sources WHERE workspace_id=? ORDER BY symbol,source`,
		workspaceID,
	)
	if err != nil {
		return AttentionSnapshot{}, err
	}
	for rows.Next() {
		var item AttentionSource
		var active int
		if err := rows.Scan(&item.Symbol, &item.Source, &active, &item.Revision, &item.IntentID, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return AttentionSnapshot{}, err
		}
		item.Active = active == 1
		snapshot.Sources = append(snapshot.Sources, item)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return AttentionSnapshot{}, rowsErr
	}
	if closeErr != nil {
		return AttentionSnapshot{}, closeErr
	}
	rows, err = tx.QueryContext(ctx,
		`SELECT symbol,revision,intent_id,updated_at FROM linkage_workspace_attention_tombstones WHERE workspace_id=? ORDER BY symbol`,
		workspaceID,
	)
	if err != nil {
		return AttentionSnapshot{}, err
	}
	for rows.Next() {
		var item AttentionTombstone
		if err := rows.Scan(&item.Symbol, &item.Revision, &item.IntentID, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return AttentionSnapshot{}, err
		}
		snapshot.Tombstones = append(snapshot.Tombstones, item)
	}
	rowsErr = rows.Err()
	closeErr = rows.Close()
	if rowsErr != nil {
		return AttentionSnapshot{}, rowsErr
	}
	if closeErr != nil {
		return AttentionSnapshot{}, closeErr
	}
	if err := tx.Commit(); err != nil {
		return AttentionSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) AttentionRevisionConflict(ctx context.Context, workspaceID, nodeID string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM linkage_workspace_attention_revisions WHERE workspace_id=?`, workspaceID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

func (s AttentionSnapshot) Validate() error {
	if s.SchemaVersion != 1 || !nodePattern.MatchString(s.WorkspaceID) || !nodePattern.MatchString(s.NodeID) {
		return fmt.Errorf("invalid attention snapshot")
	}
	return nil
}
