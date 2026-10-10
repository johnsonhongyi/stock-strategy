package linkage

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

type legacyAttentionSource struct {
	symbol   string
	source   string
	active   bool
	intentID string
}

type legacyAttentionTombstone struct {
	symbol   string
	intentID string
}

func migrateLegacyWorkspaceAttention(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`SELECT workspace_id FROM linkage_attention_revisions
		UNION SELECT workspace_id FROM linkage_attention_sources
		UNION SELECT workspace_id FROM linkage_attention_tombstones`)
	if err != nil {
		return err
	}
	workspaces := make([]string, 0)
	for rows.Next() {
		var workspaceID string
		if err := rows.Scan(&workspaceID); err != nil {
			_ = rows.Close()
			return err
		}
		if workspaceID != "" {
			workspaces = append(workspaces, workspaceID)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	sort.Strings(workspaces)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, workspaceID := range workspaces {
		var migrated string
		err := tx.QueryRow(`SELECT migrated_at FROM linkage_workspace_attention_migrations WHERE workspace_id=?`, workspaceID).Scan(&migrated)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var legacyRevision int64
		if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM linkage_attention_revisions WHERE workspace_id=?`,
			workspaceID).Scan(&legacyRevision); err != nil {
			return err
		}

		sourceRows, err := tx.Query(`SELECT symbol,source,active,intent_id
			FROM linkage_attention_sources WHERE workspace_id=? ORDER BY symbol,source,node_id`, workspaceID)
		if err != nil {
			return err
		}
		sources := make(map[string]legacyAttentionSource)
		for sourceRows.Next() {
			var item legacyAttentionSource
			var active int
			if err := sourceRows.Scan(&item.symbol, &item.source, &active, &item.intentID); err != nil {
				_ = sourceRows.Close()
				return err
			}
			item.active = active == 1
			key := item.symbol + "\x00" + item.source
			previous, exists := sources[key]
			// During migration, retain any active source so a replica with stale
			// inactive data cannot erase a valid favorite from another desktop.
			if !exists || item.active && !previous.active {
				sources[key] = item
			}
		}
		if err := sourceRows.Close(); err != nil {
			return err
		}

		tombstoneRows, err := tx.Query(`SELECT symbol,intent_id
			FROM linkage_attention_tombstones WHERE workspace_id=? ORDER BY symbol,node_id`, workspaceID)
		if err != nil {
			return err
		}
		tombstones := make(map[string]legacyAttentionTombstone)
		for tombstoneRows.Next() {
			var item legacyAttentionTombstone
			if err := tombstoneRows.Scan(&item.symbol, &item.intentID); err != nil {
				_ = tombstoneRows.Close()
				return err
			}
			if _, exists := tombstones[item.symbol]; !exists {
				tombstones[item.symbol] = item
			}
		}
		if err := tombstoneRows.Close(); err != nil {
			return err
		}

		// Start beyond every legacy desk cursor so stale local rows cannot
		// overwrite the merged workspace state during the first projection.
		revision := legacyRevision + 1
		if revision < 1 {
			revision = 1
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO linkage_workspace_attention_revisions(workspace_id,revision,updated_at) VALUES(?,?,?)`,
			workspaceID, revision, now); err != nil {
			return err
		}
		activeSymbols := make(map[string]bool)
		for _, item := range sources {
			if item.active {
				activeSymbols[item.symbol] = true
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO linkage_workspace_attention_sources
				(workspace_id,symbol,source,active,revision,intent_id,updated_at) VALUES(?,?,?,?,?,?,?)`,
				workspaceID, item.symbol, item.source, boolInt(item.active), revision, item.intentID, now); err != nil {
				return err
			}
		}
		for symbol, item := range tombstones {
			if activeSymbols[symbol] {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO linkage_workspace_attention_tombstones
				(workspace_id,symbol,revision,intent_id,updated_at) VALUES(?,?,?,?,?)`,
				workspaceID, symbol, revision, item.intentID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO linkage_workspace_attention_migrations(workspace_id,migrated_at) VALUES(?,?)`, workspaceID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
