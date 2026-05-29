package handler

import (
	"testing"
	"time"

	"leo-go/internal/token"
)

func TestCleanupTokensByStatusDeletesOnlyExhaustedTokens(t *testing.T) {
	mgr := token.NewManager(nil)
	exhaustedInfo, _, err := mgr.Add("exhausted-token", "leonardo", "session_token", "", "", "test")
	if err != nil {
		t.Fatalf("add exhausted token: %v", err)
	}
	activeInfo, _, err := mgr.Add("active-token", "leonardo", "session_token", "", "", "test")
	if err != nil {
		t.Fatalf("add active token: %v", err)
	}
	exhaustedID := toString(exhaustedInfo["id"])
	activeID := toString(activeInfo["id"])
	if err := mgr.SetStatus(exhaustedID, "exhausted"); err != nil {
		t.Fatalf("set exhausted status: %v", err)
	}

	srv := &Server{TokenMgr: mgr}
	result := srv.cleanupTokensByStatus("exhausted")
	if result.MatchedCount != 1 || result.DeletedCount != 1 || result.FailedCount != 0 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	if mgr.GetByID(exhaustedID) != nil {
		t.Fatalf("expected exhausted token to be deleted")
	}
	if mgr.GetByID(activeID) == nil {
		t.Fatalf("expected active token to remain")
	}
}

func TestCleanupTokensByStatusTreatsExpiredAsInvalid(t *testing.T) {
	mgr := token.NewManager(nil)
	expiredInfo, _, err := mgr.Add("expired-token", "leonardo", "session_token", "", "", "test")
	if err != nil {
		t.Fatalf("add expired token: %v", err)
	}
	activeInfo, _, err := mgr.Add("not-expired-token", "leonardo", "session_token", "", "", "test")
	if err != nil {
		t.Fatalf("add active token: %v", err)
	}
	expiredID := toString(expiredInfo["id"])
	activeID := toString(activeInfo["id"])
	if err := mgr.UpdateExpiry(expiredID, float64(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatalf("set expired expiry: %v", err)
	}
	if err := mgr.UpdateExpiry(activeID, float64(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatalf("set active expiry: %v", err)
	}

	srv := &Server{TokenMgr: mgr}
	result := srv.cleanupTokensByStatus("invalid")
	if result.MatchedCount != 1 || result.DeletedCount != 1 || result.FailedCount != 0 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	if mgr.GetByID(expiredID) != nil {
		t.Fatalf("expected expired token to be deleted")
	}
	if mgr.GetByID(activeID) == nil {
		t.Fatalf("expected non-expired token to remain")
	}
}
