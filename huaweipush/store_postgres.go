package huaweipush

import (
	"database/sql"
	"fmt"

	"heckel.io/ntfy/v2/db"
)

const (
	postgresCreateTablesQuery = `
		CREATE TABLE IF NOT EXISTS huawei_push_subscription (
			push_token TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			topics TEXT NOT NULL,
			updated_at BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_huawei_push_updated_at ON huawei_push_subscription (updated_at);
		CREATE TABLE IF NOT EXISTS schema_version (
			store TEXT PRIMARY KEY,
			version INT NOT NULL
		);
	`

	postgresUpsertSubscriptionQuery = `
		INSERT INTO huawei_push_subscription (push_token, project_id, topics, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (push_token)
		DO UPDATE SET project_id = excluded.project_id, topics = excluded.topics, updated_at = excluded.updated_at
	`
	postgresDeleteByTokenQuery = `DELETE FROM huawei_push_subscription WHERE push_token = $1`
	postgresSelectForTopicQuery = `
		SELECT push_token FROM huawei_push_subscription
		WHERE ',' || topics || ',' LIKE $1
		   OR topics LIKE $2
		   OR topics LIKE $3
		   OR topics = $4
	`
	postgresDeleteExpiredQuery    = `DELETE FROM huawei_push_subscription WHERE updated_at <= $1`
	postgresUpdateUpdatedAtQuery = `UPDATE huawei_push_subscription SET updated_at = $1 WHERE push_token = $2`
)

// PostgreSQL schema management queries
const (
	pgCurrentSchemaVersion           = 1
	postgresInsertSchemaVersionQuery = `INSERT INTO schema_version (store, version) VALUES ('huawei_push', $1)`
	postgresSelectSchemaVersionQuery = `SELECT version FROM schema_version WHERE store = 'huawei_push'`
)

// NewPostgresStore creates a new PostgreSQL-backed Huawei Push store using an existing database connection pool.
func NewPostgresStore(db *sql.DB) (*Store, error) {
	if err := setupPostgres(db); err != nil {
		return nil, err
	}
	return &Store{
		db: db,
		queries: queries{
			upsertSubscription: postgresUpsertSubscriptionQuery,
			deleteByToken:      postgresDeleteByTokenQuery,
			selectForTopic:     postgresSelectForTopicQuery,
			deleteExpired:      postgresDeleteExpiredQuery,
			updateUpdatedAt:    postgresUpdateUpdatedAtQuery,
		},
	}, nil
}

func setupPostgres(sqlDB *sql.DB) error {
	var schemaVersion int
	err := sqlDB.QueryRow(postgresSelectSchemaVersionQuery).Scan(&schemaVersion)
	if err != nil {
		return setupNewPostgres(sqlDB)
	}
	if schemaVersion > pgCurrentSchemaVersion {
		return fmt.Errorf("unexpected schema version: version %d is higher than current version %d", schemaVersion, pgCurrentSchemaVersion)
	}
	return nil
}

func setupNewPostgres(sqlDB *sql.DB) error {
	return db.ExecTx(sqlDB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(postgresCreateTablesQuery); err != nil {
			return err
		}
		if _, err := tx.Exec(postgresInsertSchemaVersionQuery, pgCurrentSchemaVersion); err != nil {
			return err
		}
		return nil
	})
}
