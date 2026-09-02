package app

import (
	"time"

	"namo/internal/auth"
	"namo/internal/matcher"
	"namo/internal/model"
)

// The application-level types deliberately stay small; PostgreSQL JSON fields
// retain source payload details without coupling the rest of the service to the
// public portal's response shape.
type Tenant struct {
	ID, Name string
	Created  time.Time
}

type User struct {
	ID, TenantID, Email, PasswordHash string
	Role                              auth.Role
	Created                           time.Time
}

type BidNotice = model.Notice

type NoticeRevision struct {
	NoticeID, RevisionHash string
	Payload                []byte
	CollectedAt            time.Time
}

type FilterRule = matcher.Rule

type Match = StoredMatch

type DigestSchedule struct {
	ID, TenantID, Name, TimeZone string
	Hour, Minute                 int
	Enabled                      bool
	LastSuccess                  time.Time
}

type Delivery struct {
	ID, TenantID, ScheduleID, RecipientID, Status, LastError string
	DueAt, WindowEnd, SentAt                                 time.Time
	Attempts                                                 int
}

type JobRun struct {
	ID, TenantID, Kind, Status string
	StartedAt, FinishedAt      time.Time
	Detail                     []byte
}
