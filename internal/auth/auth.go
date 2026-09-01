// Package auth provides small, storage-safe identity primitives.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Role string

const (
	PlatformAdmin Role = "platform_admin"
	TenantAdmin   Role = "tenant_admin"
	Member        Role = "member"
)

func (r Role) Valid() bool { return r == PlatformAdmin || r == TenantAdmin || r == Member }

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type Session struct {
	UserID    string
	Token     string // Send this value to the client. Never persist it.
	TokenHash string // Persist this value instead of Token.
	ExpiresAt time.Time
}

func NewSession(userID string, now time.Time, lifetime time.Duration) (Session, error) {
	token, err := newOpaqueToken()
	if err != nil {
		return Session{}, err
	}
	return Session{UserID: userID, Token: token, TokenHash: HashSessionToken(token), ExpiresAt: now.Add(lifetime)}, nil
}

func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func VerifySessionToken(storedHash, token string) bool {
	expected, err := hex.DecodeString(storedHash)
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(expected, sum[:]) == 1
}

// ValidAt checks the presented token and excludes the expiry boundary.
func (s Session) ValidAt(now time.Time, token string) bool {
	return now.Before(s.ExpiresAt) && VerifySessionToken(s.TokenHash, token)
}

func NewCSRFToken() (string, error) { return newOpaqueToken() }

func HashCSRFToken(token string) string { return HashSessionToken(token) }

func VerifyCSRFToken(storedHash, submitted string) bool {
	return VerifySessionToken(storedHash, submitted)
}

// NewInvitationToken returns the bearer value for mail and the SHA-256 value
// safe to persist. The bearer value must never be stored or logged.
func NewInvitationToken() (token, hash string, err error) {
	token, err = newOpaqueToken()
	if err != nil {
		return "", "", err
	}
	return token, HashInvitationToken(token), nil
}

func HashInvitationToken(token string) string { return HashSessionToken(token) }

func ValidInvitationToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 32
}

func newOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
