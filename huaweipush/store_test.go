package huaweipush_test

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	dbtest "heckel.io/ntfy/v2/db/test"
	"heckel.io/ntfy/v2/huaweipush"
)

func forEachBackend(t *testing.T, f func(t *testing.T, store *huaweipush.Store)) {
	t.Run("sqlite", func(t *testing.T) {
		store, err := huaweipush.NewSQLiteStore(filepath.Join(t.TempDir(), "huaweipush.db"))
		require.Nil(t, err)
		t.Cleanup(func() { store.Close() })
		f(t, store)
	})
	t.Run("postgres", func(t *testing.T) {
		testDB := dbtest.CreateTestPostgres(t)
		store, err := huaweipush.NewPostgresStore(testDB)
		require.Nil(t, err)
		f(t, store)
	})
}

func TestStore_UpsertAndQuery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		// Insert two tokens with overlapping topics
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"topicA", "topicB"}))
		require.Nil(t, store.UpsertSubscription("token2", "project1", []string{"topicB", "topicC"}))

		// Query topicA: only token1
		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Equal(t, []string{"token1"}, tokens)

		// Query topicB: both tokens
		tokens, err = store.SubscriptionsForTopic("topicB")
		require.Nil(t, err)
		sort.Strings(tokens)
		require.Equal(t, []string{"token1", "token2"}, tokens)

		// Query topicC: only token2
		tokens, err = store.SubscriptionsForTopic("topicC")
		require.Nil(t, err)
		require.Equal(t, []string{"token2"}, tokens)
	})
}

func TestStore_UpsertUpdatesTopics(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		// Insert with topicA and topicB
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"topicA", "topicB"}))

		tokens, err := store.SubscriptionsForTopic("topicB")
		require.Nil(t, err)
		require.Equal(t, []string{"token1"}, tokens)

		// Upsert same token with only topicA
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"topicA"}))

		// topicB should now return empty
		tokens, err = store.SubscriptionsForTopic("topicB")
		require.Nil(t, err)
		require.Len(t, tokens, 0)

		// topicA should still return token1
		tokens, err = store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Equal(t, []string{"token1"}, tokens)
	})
}

func TestStore_RemoveSubscription(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"topicA"}))

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 1)

		// Remove and verify empty
		require.Nil(t, store.RemoveSubscription("token1"))

		tokens, err = store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 0)
	})
}

func TestStore_RemoveByTokens(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"topicA"}))
		require.Nil(t, store.UpsertSubscription("token2", "project1", []string{"topicA"}))
		require.Nil(t, store.UpsertSubscription("token3", "project1", []string{"topicA"}))

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 3)

		// Remove two of the three tokens
		require.Nil(t, store.RemoveByTokens([]string{"token1", "token2"}))

		tokens, err = store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Equal(t, []string{"token3"}, tokens)
	})
}

func TestStore_RemoveByTokensEmpty(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		// Empty slice should not error
		require.Nil(t, store.RemoveByTokens([]string{}))

		// Nil slice should not error
		require.Nil(t, store.RemoveByTokens(nil))
	})
}

func TestStore_ExpireSubscriptions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"topicA"}))

		// Set updated_at to 10 days ago
		require.Nil(t, store.SetSubscriptionUpdatedAt("token1", time.Now().Add(-10*24*time.Hour).Unix()))

		// Expiring at 11 days threshold should NOT remove it (it was updated 10 days ago, threshold is 11)
		count, err := store.ExpireSubscriptions(11 * 24 * time.Hour)
		require.Nil(t, err)
		require.Equal(t, int64(0), count)

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 1)

		// Expiring at 9 days threshold SHOULD remove it (it was updated 10 days ago, threshold is 9)
		count, err = store.ExpireSubscriptions(9 * 24 * time.Hour)
		require.Nil(t, err)
		require.Equal(t, int64(1), count)

		tokens, err = store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 0)
	})
}

func TestStore_TopicMatchDoesNotMatchSubstring(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "project1", []string{"test"}))
		require.Nil(t, store.UpsertSubscription("token2", "project1", []string{"testing"}))

		// Query "test" should return only token1, not token2
		tokens, err := store.SubscriptionsForTopic("test")
		require.Nil(t, err)
		require.Equal(t, []string{"token1"}, tokens)

		// Query "testing" should return only token2
		tokens, err = store.SubscriptionsForTopic("testing")
		require.Nil(t, err)
		require.Equal(t, []string{"token2"}, tokens)
	})
}
