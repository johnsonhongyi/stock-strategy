package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// MigrateSQLiteFiles merges user tables from legacy SQLite files into one
// canonical database. The operation is idempotent and keeps every source file.
func MigrateSQLiteFiles(target string, sources []string) error {
	target = strings.TrimSpace(target)
	if target == "" || target == ":memory:" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create shared data directory: %w", err)
	}
	db, err := sql.Open("sqlite", target)
	if err != nil {
		return fmt.Errorf("open shared data database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; CREATE TABLE IF NOT EXISTS stock_data_migrations (source_path TEXT PRIMARY KEY, migrated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)"); err != nil {
		return fmt.Errorf("initialize shared data database: %w", err)
	}
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" || sameDatabaseFile(target, source) {
			continue
		}
		info, err := os.Stat(source)
		if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect legacy database %q: %w", source, err)
		}
		if err := migrateOne(db, source); err != nil {
			return fmt.Errorf("merge legacy database %q: %w", source, err)
		}
	}
	return nil
}

type tableDefinition struct {
	name string
	sql  string
}

type columnDefinition struct {
	name       string
	notNull    int
	defaultVal sql.NullString
	primaryKey int
}

func migrateOne(db *sql.DB, source string) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS legacy", source); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE legacy")

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	var migrated int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM main.stock_data_migrations WHERE source_path=?", absSource).Scan(&migrated)
	if err == nil {
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}

	rows, err := tx.QueryContext(ctx, "SELECT name, sql FROM legacy.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return err
	}
	var tables []tableDefinition
	for rows.Next() {
		var table tableDefinition
		if err := rows.Scan(&table.name, &table.sql); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, table := range tables {
		if strings.TrimSpace(table.sql) == "" {
			continue
		}
		var targetSQL sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT sql FROM main.sqlite_master WHERE type='table' AND name=?", table.name).Scan(&targetSQL)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == sql.ErrNoRows {
			openParen := strings.Index(table.sql, "(")
			if openParen < 0 {
				return fmt.Errorf("table %q has unsupported schema", table.name)
			}
			createSQL := "CREATE TABLE main." + quoteIdentifier(table.name) + " " + table.sql[openParen:]
			if _, err := tx.ExecContext(ctx, createSQL); err != nil {
				return fmt.Errorf("create table %q: %w", table.name, err)
			}
		}
		sourceColumns, err := tableColumns(tx, "legacy", table.name)
		if err != nil {
			return err
		}
		targetColumns, err := tableColumns(tx, "main", table.name)
		if err != nil {
			return err
		}
		targetByName := make(map[string]columnDefinition, len(targetColumns))
		for _, column := range targetColumns {
			targetByName[column.name] = column
		}
		var names []string
		sourceByName := make(map[string]bool, len(sourceColumns))
		for _, column := range sourceColumns {
			if _, exists := targetByName[column.name]; !exists {
				return fmt.Errorf("table %q has legacy column %q missing from shared schema", table.name, column.name)
			}
			names = append(names, quoteIdentifier(column.name))
			sourceByName[column.name] = true
		}
		for _, column := range targetColumns {
			if sourceByName[column.name] {
				continue
			}
			if column.notNull != 0 && !column.defaultVal.Valid && column.primaryKey == 0 {
				return fmt.Errorf("table %q requires new column %q without a default", table.name, column.name)
			}
		}
		if len(names) == 0 {
			continue
		}
		quotedTable := quoteIdentifier(table.name)
		statement := "INSERT OR IGNORE INTO main." + quotedTable + " (" + strings.Join(names, ",") + ") SELECT " + strings.Join(names, ",") + " FROM legacy." + quotedTable
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("copy table %q: %w", table.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO main.stock_data_migrations(source_path) VALUES(?)", absSource); err != nil {
		return err
	}
	return tx.Commit()
}

func tableColumns(tx *sql.Tx, schema string, table string) ([]columnDefinition, error) {
	rows, err := tx.Query("PRAGMA " + schema + ".table_info(" + quoteIdentifier(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []columnDefinition
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, columnDefinition{name: name, notNull: notNull, defaultVal: defaultValue, primaryKey: primaryKey})
	}
	return columns, rows.Err()
}

func quoteIdentifier(value string) string {
	return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
}

func sameDatabaseFile(a string, b string) bool {
	aPath, errA := filepath.Abs(a)
	bPath, errB := filepath.Abs(b)
	if errA == nil && errB == nil && aPath == bPath {
		return true
	}
	aReal, errA := filepath.EvalSymlinks(a)
	bReal, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil && aReal == bReal {
		return true
	}
	aInfo, errA := os.Stat(a)
	bInfo, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(aInfo, bInfo)
}
