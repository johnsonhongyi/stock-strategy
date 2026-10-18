package httpapi

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"
)

const desktopUsageSchema = `CREATE TABLE IF NOT EXISTS desktop_token_usage (
 job_id TEXT PRIMARY KEY, day TEXT NOT NULL, module TEXT NOT NULL, model TEXT NOT NULL,
 prompt_tokens INTEGER NOT NULL, completion_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS desktop_token_usage_period ON desktop_token_usage(day,module,model);`

// Receipts and usage commit in one transaction. The immutable job key prevents
// lease replay, reconnects, and browser retries from counting a turn twice.
// On first open, existing returned usage is recovered from retained job receipts.
const desktopUsageInsert = `INSERT OR IGNORE INTO desktop_token_usage(job_id,day,module,model,prompt_tokens,completion_tokens,total_tokens)
 SELECT id, strftime('%Y-%m-%d',COALESCE(?,created_at),'unixepoch','+8 hours'),
 CASE WHEN json_extract(body,'$.bridge_test')=1 OR json_extract(body,'$.purpose')='bridge_test' THEN 'settings-bridge-test' WHEN json_extract(body,'$.purpose')='stock_research' THEN 'stock-analysis' ELSE 'ai-chat' END,
 COALESCE(json_extract(result,'$.model'),json_extract(body,'$.model'),''),
 COALESCE(json_extract(result,'$.token_usage.inputTokens'),0),
 COALESCE(json_extract(result,'$.token_usage.outputTokens'),0),
 CASE WHEN COALESCE(json_extract(result,'$.token_usage.totalTokens'),0)>0 THEN json_extract(result,'$.token_usage.totalTokens')
 ELSE COALESCE(json_extract(result,'$.token_usage.inputTokens'),0)+COALESCE(json_extract(result,'$.token_usage.outputTokens'),0) END
 FROM desktop_chat_jobs
 WHERE status IN ('completed','failed') AND json_valid(body) AND json_valid(result)
 AND typeof(json_extract(result,'$.token_usage.inputTokens')) IN ('integer','null')
 AND typeof(json_extract(result,'$.token_usage.outputTokens')) IN ('integer','null')
 AND typeof(json_extract(result,'$.token_usage.totalTokens')) IN ('integer','null')
 AND COALESCE(json_extract(result,'$.token_usage.inputTokens'),0)>=0
 AND COALESCE(json_extract(result,'$.token_usage.outputTokens'),0)>=0
 AND COALESCE(json_extract(result,'$.token_usage.totalTokens'),0)>=0
 AND (COALESCE(json_extract(result,'$.token_usage.totalTokens'),0)>0 OR
 COALESCE(json_extract(result,'$.token_usage.inputTokens'),0)+COALESCE(json_extract(result,'$.token_usage.outputTokens'),0)>0)`

func recordDesktopUsage(tx *sql.Tx, id string, now int64) error {
	_, err := tx.Exec(desktopUsageInsert+" AND id=?", now, id)
	return err
}

func (s *Server) desktopUsageEntries(ctx context.Context) ([]tokenUsageEntry, error) {
	if s.autoFollowDBPath == "" {
		return nil, nil
	}
	path := filepath.Join(filepath.Dir(s.autoFollowDBPath), "control.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	db, err := s.desktopDB()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT day,module,model,SUM(prompt_tokens),SUM(completion_tokens),SUM(total_tokens)
 FROM desktop_token_usage GROUP BY day,module,model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []tokenUsageEntry{}
	for rows.Next() {
		var entry tokenUsageEntry
		if err := rows.Scan(&entry.Date, &entry.Module, &entry.Model, &entry.Prompt, &entry.Completion, &entry.Total); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
