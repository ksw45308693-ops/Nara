// Package digest builds safe HTML mail and schedules tenant deliveries.
package digest

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

type Notice struct{ Title, URL, Reason string }

// BuildSMTPMessage returns an RFC 5322 message body ready for an SMTP client.
func BuildSMTPMessage(from string, recipients []string, subject string, notices []Notice) ([]byte, error) {
	return BuildSMTPMessageWithID(from, recipients, subject, notices, "")
}

// BuildSMTPMessageWithID adds a stable RFC 5322 Message-ID when messageID is
// non-empty. Reusing a delivery key gives SMTP relays a consistent duplicate
// hint if the process exits after sending but before recording success.
func BuildSMTPMessageWithID(from string, recipients []string, subject string, notices []Notice, messageID string) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, fmt.Errorf("invalid subject")
	}
	if messageID != "" && !validMessageID(messageID) {
		return nil, fmt.Errorf("invalid message id")
	}
	fromAddress, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("parse sender: %w", err)
	}
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		address, err := mail.ParseAddress(recipient)
		if err != nil {
			return nil, fmt.Errorf("parse recipient: %w", err)
		}
		to = append(to, address.Address)
	}
	if len(to) == 0 {
		return nil, fmt.Errorf("at least one recipient is required")
	}
	var encoded bytes.Buffer
	writer := quotedprintable.NewWriter(&encoded)
	if _, err := writer.Write([]byte(BuildHTML(subject, notices))); err != nil {
		return nil, fmt.Errorf("encode HTML: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish HTML encoding: %w", err)
	}
	idHeader := ""
	if messageID != "" {
		idHeader = "Message-ID: <" + messageID + ">\r\n"
	}
	return []byte("From: " + fromAddress.Address + "\r\n" +
		"To: " + strings.Join(to, ", ") + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n" +
		idHeader +
		"MIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		encoded.String()), nil
}

func validMessageID(value string) bool {
	if len(value) > 254 || strings.Count(value, "@") != 1 || strings.ContainsAny(value, "<>\r\n\t ") {
		return false
	}
	left, right, _ := strings.Cut(value, "@")
	if left == "" || right == "" || strings.HasPrefix(right, ".") || strings.HasSuffix(right, ".") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".!#$%&'*+-/=?^_`{|}~@", r)) {
			return false
		}
	}
	return true
}

func BuildHTML(subject string, notices []Notice) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"ko\"><body><h1>")
	b.WriteString(html.EscapeString(subject))
	b.WriteString("</h1>")
	if len(notices) == 0 {
		b.WriteString("<p>새로 일치한 공고가 없습니다.</p>")
	}
	for _, n := range notices {
		b.WriteString("<article><h2><a href=\"")
		b.WriteString(html.EscapeString(safeURL(n.URL)))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(n.Title))
		b.WriteString("</a></h2><p>일치 사유: ")
		b.WriteString(html.EscapeString(n.Reason))
		b.WriteString("</p></article>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "#"
	}
	return raw
}

type DailySchedule struct{ Hour, Minute int }

// Due returns the latest scheduled time not yet successfully delivered.
// Schedule times are always interpreted in Asia/Seoul.
func (s DailySchedule) Due(now, lastSuccess time.Time) (time.Time, bool) {
	if s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.Time{}, false
	}
	now = now.In(loc)
	due := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, loc)
	if now.Before(due) {
		due = due.AddDate(0, 0, -1)
	}
	if !lastSuccess.IsZero() && !due.After(lastSuccess.In(loc)) {
		return time.Time{}, false
	}
	return due, true
}

func Retry3(send func() error) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = send(); err == nil {
			return nil
		}
	}
	return err
}

func DeliveryKey(tenantID, scheduleID, recipientID string, due time.Time) string {
	s := fmt.Sprintf("%s\x00%s\x00%s\x00%s", tenantID, scheduleID, recipientID, due.UTC().Format(time.RFC3339Nano))
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:])
}
