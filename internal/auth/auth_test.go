package auth

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestPasswordHashRoundTripRejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password was rejected")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
}

func TestNewSessionStoresOnlyHashAndExpires(t *testing.T) {
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	s, err := NewSession("user-1", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if s.Token == "" || s.TokenHash == s.Token {
		t.Fatal("opaque token or its hash is missing")
	}
	if !VerifySessionToken(s.TokenHash, s.Token) {
		t.Fatal("session token did not verify")
	}
	if !s.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires at = %v", s.ExpiresAt)
	}
}

func TestSessionValidityRequiresMatchingUnexpiredToken(t *testing.T) {
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	s, err := NewSession("user-1", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ValidAt(now.Add(time.Hour-1), s.Token) {
		t.Fatal("unexpired session was rejected")
	}
	if s.ValidAt(s.ExpiresAt, s.Token) {
		t.Fatal("session was valid at expiry boundary")
	}
	if s.ValidAt(now, s.Token+"x") {
		t.Fatal("wrong token was accepted")
	}
}

func TestCSRFTokenVerificationRejectsDifferentToken(t *testing.T) {
	token, err := NewCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := HashCSRFToken(token)
	if !VerifyCSRFToken(hash, token) {
		t.Fatal("same token was rejected")
	}
	if VerifyCSRFToken(hash, token+"x") {
		t.Fatal("different token was accepted")
	}
}

func TestInvitationTokenUsesThirtyTwoRandomBytesAndStoresOnlySHA256(t *testing.T) {
	token, hash, err := NewInvitationToken()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("decoded token bytes = %d, err = %v", len(raw), err)
	}
	if token == hash || len(hash) != 64 || HashInvitationToken(token) != hash {
		t.Fatalf("token/hash storage contract violated: token length=%d hash length=%d", len(token), len(hash))
	}
	if !ValidInvitationToken(token) || ValidInvitationToken(token+"x") || ValidInvitationToken("not-a-token") {
		t.Fatal("invitation token shape validation is incorrect")
	}
}

func TestRolesAreLimitedToPlatformTenantAndMember(t *testing.T) {
	for _, role := range []Role{PlatformAdmin, TenantAdmin, Member} {
		if !role.Valid() {
			t.Fatalf("%q is invalid", role)
		}
	}
	if Role("owner").Valid() {
		t.Fatal("unknown role is valid")
	}
}
