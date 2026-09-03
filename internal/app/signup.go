package app

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"
)

// Self-service signup collects an email and a password only. The account starts
// without a tenant, so a platform administrator decides which company data the
// member may reach.
const (
	minSignupPasswordBytes = 12
	maxSignupPasswordBytes = 72
)

var (
	ErrEmailRegistered  = errors.New("email already belongs to an account")
	ErrInvitationWaits  = errors.New("invitation is already pending for this email")
	ErrAccountUnknown   = errors.New("member account is unavailable")
	ErrTenantUnknown    = errors.New("tenant is unavailable")
	ErrSignupPrivileges = errors.New("platform administrator role is required")
)

// SignupInput is one validated account creation request.
type SignupInput struct{ Email, PasswordHash string }

// MemberAccount is one member account and its current tenant assignment.
type MemberAccount struct {
	UserID, Email, DisplayName, TenantID, TenantName string
	Created                                          time.Time
}

// SignupRepository creates self-service accounts.
type SignupRepository interface {
	CreateAccount(context.Context, SignupInput) (LoginAccount, error)
}

// AccountAssignmentRepository reads and changes member tenant assignments.
type AccountAssignmentRepository interface {
	MemberAccounts(context.Context, string) ([]MemberAccount, error)
	SetAccountTenant(context.Context, string, string, string) error
}

func validSignupPassword(password string) bool {
	size := len([]byte(password))
	return utf8.ValidString(password) && size >= minSignupPasswordBytes && size <= maxSignupPasswordBytes
}
