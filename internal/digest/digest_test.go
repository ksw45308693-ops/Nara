package digest

import (
	"bytes"
	"errors"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestBuildHTMLDigestEscapesNoticeData(t *testing.T) {
	html := BuildHTML("오늘의 공고", []Notice{{Title: "A < B", URL: "https://example.test/?a=1&b=2", Reason: "금액 조건"}})
	if !strings.Contains(html, "A &lt; B") || strings.Contains(html, "A < B") {
		t.Fatalf("title was not escaped: %s", html)
	}
	if !strings.Contains(html, "오늘의 공고") || !strings.Contains(html, "금액 조건") {
		t.Fatalf("digest is incomplete: %s", html)
	}
}

func TestBuildSMTPMessageUsesHTMLUTF8Body(t *testing.T) {
	message, err := BuildSMTPMessage("monitor@example.test", []string{"staff@example.test"}, "오늘의 공고", nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(message))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Content-Type") != "text/html; charset=UTF-8" || parsed.Header.Get("Content-Transfer-Encoding") != "quoted-printable" || parsed.Header.Get("To") != "staff@example.test" {
		t.Fatalf("invalid headers: %#v", parsed.Header)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "새로 일치한 공고가 없습니다.") {
		t.Fatalf("Korean body did not round-trip: %s", body)
	}
}

func TestBuildSMTPMessageWithIDAddsStableSafeHeader(t *testing.T) {
	message, err := BuildSMTPMessageWithID("monitor@example.test", []string{"staff@example.test"}, "오늘의 공고", nil, "abc123@namo.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(message); !strings.Contains(got, "Message-ID: <abc123@namo.invalid>\r\n") {
		t.Fatalf("message id missing: %s", got)
	}
	if _, err := BuildSMTPMessageWithID("monitor@example.test", []string{"staff@example.test"}, "오늘의 공고", nil, "bad\r\nBcc: victim@example.test"); err == nil {
		t.Fatal("header injection in message id was accepted")
	}
}

func TestBuildHTMLDigestRejectsHostlessURL(t *testing.T) {
	html := BuildHTML("공고", []Notice{{Title: "공고", URL: "https:///missing-host"}})
	if strings.Contains(html, "https:///missing-host") || !strings.Contains(html, "href=\"#\"") {
		t.Fatalf("hostless URL was accepted: %s", html)
	}
}

func TestScheduleDueReturnsMissedDailyRunInSeoul(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 9, 5, 0, 0, loc)
	due, ok := DailySchedule{Hour: 9, Minute: 0}.Due(now, time.Date(2026, 9, 1, 9, 0, 0, 0, loc))
	if !ok || !due.Equal(time.Date(2026, 9, 2, 9, 0, 0, 0, loc)) {
		t.Fatalf("due = %v, %t", due, ok)
	}
}

func TestScheduleDueBeforeEqualInvalidAndMultiDayCases(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	s := DailySchedule{Hour: 9, Minute: 0}
	before := time.Date(2026, 9, 2, 8, 59, 0, 0, loc)
	due, ok := s.Due(before, time.Time{})
	if !ok || !due.Equal(time.Date(2026, 9, 1, 9, 0, 0, 0, loc)) {
		t.Fatalf("before due = %v, %t", due, ok)
	}
	equal := time.Date(2026, 9, 2, 9, 0, 0, 0, loc)
	due, ok = s.Due(equal, time.Time{})
	if !ok || !due.Equal(equal) {
		t.Fatalf("equal due = %v, %t", due, ok)
	}
	if _, ok = (DailySchedule{Hour: 24}).Due(equal, time.Time{}); ok {
		t.Fatal("invalid schedule is due")
	}
	if _, ok = s.Due(time.Date(2026, 9, 4, 10, 0, 0, 0, loc), time.Date(2026, 9, 2, 9, 0, 0, 0, loc)); !ok {
		t.Fatal("multi-day catch-up was rejected")
	}
}

func TestRetryAttemptsThreeTimes(t *testing.T) {
	attempts := 0
	err := Retry3(func() error { attempts++; return errors.New("smtp unavailable") })
	if err == nil || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRetryStopsAtFirstSuccess(t *testing.T) {
	attempts := 0
	err := Retry3(func() error {
		attempts++
		if attempts == 2 {
			return nil
		}
		return errors.New("temporary")
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestDeliveryKeyIsStableAndRecipientSpecific(t *testing.T) {
	a := DeliveryKey("tenant-1", "schedule-1", "recipient-1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	b := DeliveryKey("tenant-1", "schedule-1", "recipient-1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	c := DeliveryKey("tenant-1", "schedule-1", "recipient-2", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if a != b || a == c {
		t.Fatalf("keys: %q %q %q", a, b, c)
	}
}
