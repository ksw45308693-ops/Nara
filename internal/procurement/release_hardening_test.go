package procurement

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"g2b-monitor/internal/model"
)

type callBudgetStub struct {
	takes int
	err   error
}

func (b *callBudgetStub) Take(context.Context) error {
	b.takes++
	return b.err
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientConsumesBudgetBeforeEveryHTTPAttempt(t *testing.T) {
	budget := &callBudgetStub{}
	attempts := 0
	client := NewClient(Config{
		BaseURL:       "https://example.invalid",
		ServiceKey:    "top-secret-key",
		CallBudget:    budget,
		RetryAttempts: 3,
		Wait:          func(context.Context, time.Duration) error { return nil },
		HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("socket failed")
		})},
	})
	_, _ = client.List(context.Background(), model.CategoryGoods, ListQuery{})
	if budget.takes != 3 || attempts != 3 {
		t.Fatalf("budget takes=%d HTTP attempts=%d, want 3 each", budget.takes, attempts)
	}
}

func TestClientDoesNotSendWhenDailyBudgetIsExhausted(t *testing.T) {
	budgetErr := errors.New("daily budget exhausted")
	budget := &callBudgetStub{err: budgetErr}
	attempts := 0
	client := NewClient(Config{
		BaseURL:    "https://example.invalid",
		ServiceKey: "top-secret-key",
		CallBudget: budget,
		HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("must not run")
		})},
	})
	_, err := client.List(context.Background(), model.CategoryGoods, ListQuery{})
	if !errors.Is(err, budgetErr) || budget.takes != 1 || attempts != 0 {
		t.Fatalf("error=%v budget takes=%d HTTP attempts=%d", err, budget.takes, attempts)
	}
}

func TestClientDoesNotFollowUnbudgetedRedirects(t *testing.T) {
	budget := &callBudgetStub{}
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	_, err := NewClient(Config{BaseURL: source.URL, ServiceKey: "secret", CallBudget: budget, HTTPClient: source.Client()}).List(context.Background(), model.CategoryGoods, ListQuery{})
	if err == nil || budget.takes != 1 || redirected != 0 {
		t.Fatalf("error=%v budget takes=%d redirected requests=%d", err, budget.takes, redirected)
	}
}

func TestClientTransportAndRequestErrorsNeverExposeURLOrServiceKey(t *testing.T) {
	const secret = "top-secret-key"
	client := NewClient(Config{
		BaseURL:       "https://example.invalid/private-path",
		ServiceKey:    secret,
		RetryAttempts: 1,
		HTTPClient: &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: request.URL.String(), Err: errors.New("socket failed")}
		})},
	})
	_, err := client.List(context.Background(), model.CategoryGoods, ListQuery{})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "example.invalid") || strings.Contains(err.Error(), "serviceKey") {
		t.Fatalf("transport error leaked request data: %q", err)
	}

	client = NewClient(Config{BaseURL: "://" + secret, ServiceKey: secret})
	_, err = client.List(context.Background(), model.CategoryGoods, ListQuery{})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "serviceKey") {
		t.Fatalf("request construction error leaked request data: %q", err)
	}
}

func TestServiceErrorRedactsEchoedServiceKey(t *testing.T) {
	const secret = "top-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"` + secret + `","resultMsg":"invalid ` + secret + `"}}}`))
	}))
	defer server.Close()
	_, err := NewClient(Config{BaseURL: server.URL, ServiceKey: secret, HTTPClient: server.Client()}).List(context.Background(), model.CategoryGoods, ListQuery{})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("service error leaked key: %q", err)
	}
}

func TestListQuarantinesMalformedFieldsWithBoundedSanitizedRawJSON(t *testing.T) {
	const secret = "must-not-be-stored"
	var warnings []FieldWarning
	client := NewClient(Config{
		BaseURL: "https://example.invalid",
		HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			body := `{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[` +
				`{"bidNtceNo":"N-AMOUNT","bidNtceOrd":"00","bidNtceNm":"bad amount","presmptPrce":"not-money","serviceKey":"` + secret + `"},` +
				`{"bidNtceNo":"N-POSTED","bidNtceOrd":"00","bidNtceNm":"bad posted","bidNtceDt":"not-time"},` +
				`{"bidNtceNo":"N-DEADLINE","bidNtceOrd":"00","bidNtceNm":"bad deadline","bidClseDt":"not-time"},` +
				`{"bidNtceNo":"","bidNtceOrd":"00","bidNtceNm":"invalid","password":"` + secret + `"},` +
				`{"bidNtceNo":"N-OK","bidNtceOrd":"00","bidNtceNm":"valid optional blanks"}` +
				`]},"totalCount":5}}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
		Warning: func(warning FieldWarning) { warnings = append(warnings, warning) },
	})
	notices, err := client.List(context.Background(), model.CategoryGoods, ListQuery{})
	if err != nil || len(notices) != 1 || notices[0].BidNumber != "N-OK" {
		t.Fatalf("notices=%+v error=%v", notices, err)
	}
	if notices[0].RawJSON == nil || len(notices[0].RawJSON) > maxWarningRawBytes {
		t.Fatalf("raw JSON length=%d", len(notices[0].RawJSON))
	}
	joined := string(notices[0].RawJSON)
	for _, warning := range warnings {
		joined += string(warning.RawJSON)
	}
	if strings.Contains(joined, secret) || strings.Contains(strings.ToLower(joined), "servicekey") || strings.Contains(strings.ToLower(joined), "password") {
		t.Fatalf("sanitized raw data leaked a secret: %s", joined)
	}
	if !warningFieldCode(warnings, "amount", "invalid_amount") ||
		!warningFieldCode(warnings, "posted_at", "invalid_time") ||
		!warningFieldCode(warnings, "deadline", "invalid_time") ||
		!warningCode(warnings, "missing_required") {
		t.Fatalf("warnings=%+v", warnings)
	}
}

func TestListRedactsServiceKeyFromNestedNonSecretRawValues(t *testing.T) {
	const secret = "raw+/="
	escaped := url.QueryEscape(secret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":` +
			`{"bidNtceNo":"N-RAW","bidNtceOrd":"00","bidNtceNm":"safe notice",` +
			`"metadata":{"message":"prefix ` + secret + ` suffix ` + escaped + `","values":["` + secret + `"],` +
			`"padding":"` + secret + strings.Repeat("x", maxWarningRawBytes+1000) + `"}}},"totalCount":1}}}`))
	}))
	defer server.Close()

	notices, err := NewClient(Config{BaseURL: server.URL, ServiceKey: secret, HTTPClient: server.Client()}).List(context.Background(), model.CategoryGoods, ListQuery{})
	if err != nil || len(notices) != 1 {
		t.Fatalf("notices=%+v error=%v", notices, err)
	}
	raw := string(notices[0].RawJSON)
	if len(raw) > maxWarningRawBytes || strings.Contains(raw, secret) || strings.Contains(raw, escaped) || !strings.Contains(raw, "[redacted]") || !strings.Contains(raw, `"truncated":true`) {
		t.Fatalf("nested raw value was not redacted: %s", raw)
	}
}

func TestListDoesNotCopyServiceKeyFromSourceURLIntoNotice(t *testing.T) {
	const secret = "raw+/="
	escaped := url.QueryEscape(secret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":` +
			`{"bidNtceNo":"N-URL","bidNtceOrd":"00","bidNtceNm":"safe notice",` +
			`"bidNtceDtlUrl":"https://example.invalid/detail?serviceKey=` + escaped + `&notice=N-URL"}` +
			`},"totalCount":1}}}`))
	}))
	defer server.Close()

	notices, err := NewClient(Config{BaseURL: server.URL, ServiceKey: secret, HTTPClient: server.Client()}).List(context.Background(), model.CategoryGoods, ListQuery{})
	if err != nil || len(notices) != 1 {
		t.Fatalf("notices=%+v error=%v", notices, err)
	}
	encoded, err := json.Marshal(notices[0])
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(encoded)
	if strings.Contains(serialized, secret) || strings.Contains(serialized, escaped) {
		t.Fatalf("marshaled notice leaked service key: %s", serialized)
	}
}

func TestSanitizedRawJSONPreservesLargeIntegerAndRejectsTrailingToken(t *testing.T) {
	const largeInteger = "9007199254740993"
	sanitized := sanitizedRawJSON(json.RawMessage(`{"identifier":` + largeInteger + `}`))
	if !strings.Contains(string(sanitized), largeInteger) {
		t.Fatalf("large integer changed during sanitization: %s", sanitized)
	}

	trailing := sanitizedRawJSON(json.RawMessage(`{"identifier":` + largeInteger + `} true`))
	if strings.Contains(string(trailing), largeInteger) || !strings.Contains(string(trailing), "invalid JSON omitted") {
		t.Fatalf("trailing JSON token was accepted: %s", trailing)
	}
}

func TestParseAmountRejectsNegativeValues(t *testing.T) {
	if amount, ok := parseAmount("-1"); ok || amount != -1 {
		t.Fatalf("parseAmount(-1) = %d, %t; want quarantined negative value", amount, ok)
	}
	if amount, ok := parseAmount("0"); !ok || amount != 0 {
		t.Fatalf("parseAmount(0) = %d, %t", amount, ok)
	}
	_, warnings, valid := (apiNotice{
		BidNumber: "NEG-1", BidSequence: "00", Title: "잘못된 금액", Amount: "-1",
	}).notice(model.CategoryGoods, json.RawMessage(`{"presmptPrce":"-1"}`))
	if valid || !warningFieldCode(warnings, "amount", "invalid_amount") {
		t.Fatalf("negative amount valid=%t warnings=%+v", valid, warnings)
	}
}

func warningFieldCode(warnings []FieldWarning, field, code string) bool {
	for _, warning := range warnings {
		if warning.Field == field && warning.Code == code && len(warning.RawJSON) > 0 {
			return true
		}
	}
	return false
}

func warningCode(warnings []FieldWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code && len(warning.RawJSON) > 0 {
			return true
		}
	}
	return false
}
