package token

import (
	"testing"
)

type memoryTokenStore struct {
	rows []map[string]interface{}
}

func (s *memoryTokenStore) LoadTokens() ([]map[string]interface{}, error) {
	return append([]map[string]interface{}(nil), s.rows...), nil
}

func (s *memoryTokenStore) ReplaceTokens(tokens []map[string]interface{}) error {
	s.rows = append([]map[string]interface{}(nil), tokens...)
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
