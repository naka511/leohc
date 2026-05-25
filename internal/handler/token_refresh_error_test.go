package handler

import (
	"errors"
	"testing"
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
