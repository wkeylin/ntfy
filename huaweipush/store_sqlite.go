package huaweipush

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3" // SQLite driver

	"heckel.io/ntfy/v2/db"
)

const (
	sqliteCreateTablesQuery = `
		CREATE TABLE IF NOT EXISTS huawei_push_subscription (
			push_token TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			topics TEXT NOT NULL,
			updated_at INT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_huawei_push_updated_at ON huawei_push_subscription (updated_at);
		CREATE TABLE IF NOT EXISTS schemaVersion (
			id INT PRIMARY KEY,
			version INT NOT NULL
		);
	`

	sqliteUpsertSubscriptionQuery = `
		INSERT INTO huawei_push_subscription (push_token, project_id, topics, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (push_token)
		DO UPDATE SET project_id = excluded.project_id, topics = excluded.topics, updated_at = excluded.updated_at
	`
	sqliteDeleteByTokenQuery = `DELETE FROM huawei_push_subscription WHERE push_token = ?`
	sqliteSelectForTopicQuery = `
		SELECT push_token FROM huawei_push_subscription
		WHERE ',' || topics || ',' LIKE ?
		   OR topics LIKE ?
		   OR topics LIKE ?
		   OR topics = ?
	`
	sqliteDeleteExpiredQuery    = `DELETE FROM huawei_push_subscription WHERE updated_at <= ?`
	sqliteUpdateUpdatedAtQuery = `UPDATE huawei_push_subscription SET updated_at = ? WHERE push_token = ?`
)

// SQLite schema management queries
const (
	sqliteCurrentSchemaVersion     = 1
	sqliteInsertSchemaVersionQuery = `INSERT INTO schemaVersion VALUES (1, ?)`
	sqliteSelectSchemaVersionQuery = `SELECT version FROM schemaVersion WHERE id = 1`
)

// NewSQLiteStore creates a new SQLite-backed Huawei Push store.
func NewSQLiteStore(filename string) (*Store, error) {
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, err
	}
	if err := setupSQLite(db); err != nil {
		return nil, err
	}
	return &Store{
		db: db,
		queries: queries{
			upsertSubscription: sqliteUpsertSubscriptionQuery,
			deleteByToken:      sqliteDeleteByTokenQuery,
			selectForTopic:     sqliteSelectForTopicQuery,
			deleteExpired:      sqliteDeleteExpiredQuery,
			updateUpdatedAt:    sqliteUpdateUpdatedAtQuery,
		},
	}, nil
}

func setupSQLite(sqlDB *sql.DB) error {
	var schemaVersion int
	if err := sqlDB.QueryRow(sqliteSelectSchemaVersionQuery).Scan(&schemaVersion); err != nil {
		return setupNewSQLite(sqlDB)
	} else if schemaVersion > sqliteCurrentSchemaVersion {
		return fmt.Errorf("unexpected schema version: version %d is higher than current version %d", schemaVersion, sqliteCurrentSchemaVersion)
	}
	return nil
}

func setupNewSQLite(sqlDB *sql.DB) error {
	return db.ExecTx(sqlDB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(sqliteCreateTablesQuery); err != nil {
			return err
		}
		if _, err := tx.Exec(sqliteInsertSchemaVersionQuery, sqliteCurrentSchemaVersion); err != nil {
			return err
		}
		return nil
	})
}
