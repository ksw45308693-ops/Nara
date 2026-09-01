package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

type SourceValidationError struct{ Field string }

func (e *SourceValidationError) Error() string { return "missing required notice field: " + e.Field }

type Category string

const (
	CategoryConstruction Category = "construction"
	CategoryService      Category = "service"
	CategoryGoods        Category = "goods"
	CategoryForeign      Category = "foreign"
)

type Notice struct {
	Category    Category
	BidNumber   string
	BidSequence string
	Title       string
	Agency      string
	Region      string
	SourceURL   string
	Amount      int64
	PostedAt    time.Time
	Deadline    time.Time
	// RawJSON is the bounded, secret-scrubbed source item retained for audit and
	// revision history. It is never populated with request URLs or credentials.
	RawJSON json.RawMessage
}

func NormalizeText(value string) string { return strings.TrimSpace(norm.NFC.String(value)) }

func (n Notice) ValidateSource() error {
	if n.Category != CategoryConstruction && n.Category != CategoryService && n.Category != CategoryGoods && n.Category != CategoryForeign {
		return &SourceValidationError{Field: "category"}
	}
	if NormalizeText(n.BidNumber) == "" {
		return &SourceValidationError{Field: "bid number"}
	}
	if NormalizeText(n.BidSequence) == "" {
		return &SourceValidationError{Field: "bid sequence"}
	}
	if NormalizeText(n.Title) == "" {
		return &SourceValidationError{Field: "title"}
	}
	return nil
}

func (n Notice) Identity() string {
	if n.ValidateSource() != nil {
		return ""
	}
	return hash(string(n.Category), NormalizeText(n.BidNumber), NormalizeText(n.BidSequence))
}

func (n Notice) Revision() string {
	if n.Identity() == "" {
		return ""
	}
	return hash(
		n.Identity(), NormalizeText(n.Title), NormalizeText(n.Agency), NormalizeText(n.Region),
		strconv.FormatInt(n.Amount, 10), NormalizeText(n.SourceURL), n.PostedAt.UTC().Format(time.RFC3339Nano), n.Deadline.UTC().Format(time.RFC3339Nano), revisionRawJSON(n.RawJSON),
	)
}

func revisionRawJSON(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "null" {
		return ""
	}
	return value
}

func hash(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}
