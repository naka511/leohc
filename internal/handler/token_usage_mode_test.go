package handler

import (
	"testing"

	"leo-go/internal/config"
	"leo-go/internal/token"
)

func TestTokenUsageModeDefaultsToVideoKo3(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	srv := &Server{Config: cfg}
	if got := srv.tokenMaxRunningTasks(); got != defaultTokenMaxRunningTasks {
		t.Fatalf("tokenMaxRunningTasks() = %d, want %d", got, defaultTokenMaxRunningTasks)
	}
	if got := srv.tokenExhaustionCreditThreshold(); got != float64(videoKo3ExhaustionCredits) {
		t.Fatalf("tokenExhaustionCreditThreshold() = %.0f, want %.0f", got, float64(videoKo3ExhaustionCredits))
	}
}

func TestTokenUsageModeSora2Dedicated(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{
		"sora2_dedicated_mode_enabled": true,
	})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	srv := &Server{Config: cfg}
	if got := srv.tokenMaxRunningTasks(); got != sora2TokenMaxRunningTasks {
		t.Fatalf("tokenMaxRunningTasks() = %d, want %d", got, sora2TokenMaxRunningTasks)
	}
	if got := srv.tokenExhaustionCreditThreshold(); got != float64(sora2ExhaustionCredits) {
		t.Fatalf("tokenExhaustionCreditThreshold() = %.0f, want %.0f", got, float64(sora2ExhaustionCredits))
	}
}

func TestTokenCanRunSora2UsesModeThreshold(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	mgr := token.NewManager(nil)
	info, _, err := mgr.Add("sora2-threshold-token", "leonardo", "session_token", "", "", "test")
	if err != nil {
		t.Fatalf("add token: %v", err)
	}
	tokenID := toString(info["id"])
	if err := mgr.UpdateCredits(tokenID, 2000, 7200); err != nil {
		t.Fatalf("update credits: %v", err)
	}

	cfg.SetAll(map[string]interface{}{
		"sora2_dedicated_mode_enabled": false,
	})
	srv := &Server{Config: cfg, TokenMgr: mgr}
	if srv.tokenCanRunModelByCredits(mgr.GetByID(tokenID), "sora2", false) {
		t.Fatalf("expected 2000-credit token to be rejected in video+ko3 mode")
	}
	if status := toString(mgr.GetByID(tokenID)["status"]); status != "exhausted" {
		t.Fatalf("status = %q, want exhausted", status)
	}

	if err := mgr.SetStatus(tokenID, "active"); err != nil {
		t.Fatalf("reset status: %v", err)
	}
	cfg.SetAll(map[string]interface{}{
		"sora2_dedicated_mode_enabled": true,
	})
	if !srv.tokenCanRunModelByCredits(mgr.GetByID(tokenID), "sora2", false) {
		t.Fatalf("expected 2000-credit token to be accepted in sora2 dedicated mode")
	}
}
