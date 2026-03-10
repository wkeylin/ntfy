package huaweipush

import (
	"database/sql"
	"strings"
	"time"

	"heckel.io/ntfy/v2/db"
)

// Store holds the database connection and queries for Huawei Push subscriptions.
type Store struct {
	db      *sql.DB
	queries queries
}

// queries holds the database-specific SQL queries.
type queries struct {
	upsertSubscription string
	deleteByToken      string
	selectForTopic     string
	deleteExpired      string
	updateUpdatedAt    string
}

// UpsertSubscription adds or updates a Huawei Push subscription for the given topics.
func (s *Store) UpsertSubscription(pushToken, projectID string, topics []string) error {
	topicsStr := strings.Join(topics, ",")
	updatedAt := time.Now().Unix()
	_, err := s.db.Exec(s.queries.upsertSubscription, pushToken, projectID, topicsStr, updatedAt)
	return err
}

// RemoveSubscription removes a subscription by push token.
func (s *Store) RemoveSubscription(pushToken string) error {
	_, err := s.db.Exec(s.queries.deleteByToken, pushToken)
	return err
}

// RemoveByTokens removes subscriptions for the given push tokens within a transaction.
func (s *Store) RemoveByTokens(pushTokens []string) error {
	return db.ExecTx(s.db, func(tx *sql.Tx) error {
		for _, token := range pushTokens {
			if _, err := tx.Exec(s.queries.deleteByToken, token); err != nil {
				return err
			}
		}
		return nil
	})
}

// SubscriptionsForTopic returns all push tokens subscribed to the given topic.
// Topics are stored as comma-separated strings, so we use LIKE patterns to match
// exact topic names (middle of list, start of list, end of list, or only item).
func (s *Store) SubscriptionsForTopic(topic string) ([]string, error) {
	rows, err := s.db.Query(s.queries.selectForTopic, "%,"+topic+",%", topic+",%", "%,"+topic, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := make([]string, 0)
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// ExpireSubscriptions removes subscriptions not updated within the given duration
// and returns the number of deleted rows.
func (s *Store) ExpireSubscriptions(olderThan time.Duration) (int64, error) {
	result, err := s.db.Exec(s.queries.deleteExpired, time.Now().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// SetSubscriptionUpdatedAt updates the updated_at timestamp for a subscription by push token.
func (s *Store) SetSubscriptionUpdatedAt(pushToken string, updatedAt int64) error {
	_, err := s.db.Exec(s.queries.updateUpdatedAt, updatedAt, pushToken)
	return err
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
