package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"g2b-monitor/internal/model"
)

type countingCallBudget struct{ calls int }

func (b *countingCallBudget) Take(context.Context) error {
	b.calls++
	return nil
}

func TestProcurementSourceReturnsNormalizedWarnings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"bidNtceNo":"N-1","bidNtceOrd":"00","bidNtceNm":"샘플 공고"}]},"totalCount":1}}}`))
	}))
	defer server.Close()

	source := ProcurementSource{BaseURL: server.URL, ServiceKey: "decoded-key", HTTPClient: server.Client()}
	result, err := source.Fetch(context.Background(), model.CategoryGoods, time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Notices) != 1 || result.Notices[0].Identity() == "" {
		t.Fatalf("notices = %+v", result.Notices)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Category != model.CategoryGoods || result.Warnings[0].Field != "region" || len(result.Warnings[0].RawJSON) == 0 {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestProcurementSourceSharesCallBudgetAcrossListAndRegion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getBidPblancListInfoPrtcptPsblRgn" {
			_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"prtcptPsblRgnNm":"부산"}]},"totalCount":1}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"bidNtceNo":"N-1","bidNtceOrd":"00","bidNtceNm":"샘플 공고"}]},"totalCount":1}}}`))
	}))
	defer server.Close()

	budget := &countingCallBudget{}
	source := ProcurementSource{
		BaseURL: server.URL, ServiceKey: "decoded-key", HTTPClient: server.Client(),
		CallBudget: budget,
	}
	if _, err := source.Fetch(context.Background(), model.CategoryGoods, time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	region, err := source.LookupRegion(context.Background(), "N-1", "00")
	if err != nil || region != "부산" {
		t.Fatalf("region=%q err=%v", region, err)
	}
	if budget.calls != 2 {
		t.Fatalf("shared budget calls=%d, want 2", budget.calls)
	}
}

func TestSMTPMailerRejectsInvalidAddressAndHonorsCanceledContext(t *testing.T) {
	mailer := SMTPMailer{Host: "127.0.0.1", Port: 25}
	if err := mailer.Send(context.Background(), "bad\n@example.com", "to@example.com", []byte("message")); err == nil {
		t.Fatal("header-injection sender must be rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mailer.Send(ctx, "from@example.com", "to@example.com", []byte("message")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}
