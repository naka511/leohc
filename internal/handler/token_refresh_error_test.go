package handler

import (
	"errors"
	"testing"

	"leo-go/internal/token"
)

func TestNoJWTSessionRefreshErrorIsAbnormal(t *testing.T) {
	err := errors.New("token validation failed: ensure JWT: no JWT found in session response, body keys: [session user]")

	if !shouldMarkTokenAbnormalOnRefreshError(err) {
		t.Fatal("expected no JWT session response to be treated as abnormal")
	}
	if !isAbnormalLeonardoTokenError(err) {
		t.Fatal("expected no JWT session response to be an abnormal Leonardo token error")
	}
	if isInvalidLeonardoTokenError(err) {
		t.Fatal("expected no JWT session response not to be treated as invalid")
	}
}

func TestNoJWTSessionRefreshErrorConfirmsImmediately(t *testing.T) {
	err := errors.New("token validation failed: no JWT found in session response, body keys []")
	mgr := token.NewManager(&memoryTokenStore{})
	info, _, addErr := mgr.Add("cookie-a", "leonardo", "session_token", "", "", "")
	if addErr != nil {
		t.Fatalf("add token: %v", addErr)
	}
	tokenID := toString(info["id"])
	srv := &Server{TokenMgr: mgr}

	result := srv.recordLeonardoRefreshFailure(tokenID, err)
	if result.failCount != 1 {
		t.Fatalf("failCount = %d, want 1", result.failCount)
	}
	if result.finalStatus != "abnormal" {
		t.Fatalf("finalStatus = %q, want abnormal", result.finalStatus)
	}
	updated := mgr.GetByID(tokenID)
	if got := toString(updated["status"]); got != "abnormal" {
		t.Fatalf("status = %q, want abnormal", got)
	}
	if toBool(updated["auto_refresh"]) {
		t.Fatal("auto_refresh should be disabled")
	}
}
