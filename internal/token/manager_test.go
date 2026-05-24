package token

import (
	"testing"
)

type memoryTokenStore struct {
	rows  []map[string]interface{}
	saves int
}

func (s *memoryTokenStore) LoadTokens() ([]map[string]interface{}, error) {
	return append([]map[string]interface{}(nil), s.rows...), nil
}

func (s *memoryTokenStore) ReplaceTokens(tokens []map[string]interface{}) error {
	s.rows = append([]map[string]interface{}(nil), tokens...)
	s.saves++
	return nil
}

func TestRemoveManyRemovesTokensWithMissingCount(t *testing.T) {
	t.Parallel()

	m := NewManager(&memoryTokenStore{})
	if _, _, err := m.Add("token-a", "leonardo", "session_token", "", "", ""); err != nil {
		t.Fatalf("add token a: %v", err)
	}
	if _, _, err := m.Add("token-b", "leonardo", "session_token", "", "", ""); err != nil {
		t.Fatalf("add token b: %v", err)
	}
	if _, _, err := m.Add("token-c", "leonardo", "session_token", "", "", ""); err != nil {
		t.Fatalf("add token c: %v", err)
	}

	deletedIDs, missing := m.RemoveMany([]string{
		GenerateTokenID("token-a"),
		"missing-token",
		GenerateTokenID("token-c"),
		GenerateTokenID("token-a"),
		"",
	})
	if missing != 1 {
		t.Fatalf("expected 1 missing token, got %d", missing)
	}
	if len(deletedIDs) != 2 {
		t.Fatalf("expected 2 deleted tokens, got %d (%v)", len(deletedIDs), deletedIDs)
	}

	remaining := m.ListFull()
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining token, got %d", len(remaining))
	}
	if got := remaining[0]["id"]; got != GenerateTokenID("token-b") {
		t.Fatalf("expected token-b to remain, got %v", got)
	}
}

func TestUpsertImportedCookiesSavesOnceAndMarksPending(t *testing.T) {
	t.Parallel()

	store := &memoryTokenStore{}
	m := NewManager(store)

	results := m.UpsertImportedCookies([]ImportedCookieInput{
		{Value: "cookie-a", AccountName: "A", Source: "api_cookie_import", AutoRefresh: true, Status: "pending"},
		{Value: "cookie-b", AccountName: "B", Source: "api_cookie_import", AutoRefresh: true, Status: "pending"},
	})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if store.saves != 1 {
		t.Fatalf("expected one save for batch import, got %d", store.saves)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("unexpected import error: %v", result.Err)
		}
		if got := result.Info["status"]; got != "pending" {
			t.Fatalf("expected pending status, got %v", got)
		}
	}
}

func TestUpsertImportedCookiesKeepsActiveDuplicateActive(t *testing.T) {
	t.Parallel()

	m := NewManager(&memoryTokenStore{})
	if _, _, _, err := m.UpsertImportedCookie("cookie-a", "A", "", "", "api_cookie_import", true); err != nil {
		t.Fatalf("upsert active cookie: %v", err)
	}

	results := m.UpsertImportedCookies([]ImportedCookieInput{
		{Value: "cookie-a", AccountName: "A", Source: "api_cookie_import", AutoRefresh: true, Status: "pending"},
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Duplicate {
		t.Fatalf("expected duplicate result")
	}
	if got := results[0].Info["status"]; got != "active" {
		t.Fatalf("expected active duplicate to remain active, got %v", got)
	}
}
