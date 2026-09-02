package procurement

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"namo/internal/model"
)

func TestListPaginatesOfficialConstructionJSON(t *testing.T) {
	pageOne, err := os.ReadFile("testdata/sanitized-construction-page.json")
	if err != nil {
		t.Fatal(err)
	}
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("pageNo"))
		if r.URL.Path != "/getBidPblancListInfoCnstwk" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("serviceKey") != "test-key" || r.URL.Query().Get("type") != "json" || r.URL.Query().Get("inqryDiv") != "1" {
			t.Fatalf("official request query = %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("pageNo") == "2" {
			_, _ = w.Write([]byte(strings.Replace(string(pageOne), "SANITIZED-001", "SANITIZED-002", 1)))
			return
		}
		_, _ = w.Write(pageOne)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, ServiceKey: "test-key", HTTPClient: server.Client()})
	notices, err := client.List(context.Background(), model.CategoryConstruction, ListQuery{PageSize: 1, StartDate: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(notices), 2; got != want {
		t.Fatalf("notice count = %d, want %d", got, want)
	}
	if notices[0].Title != "서울 도로 설계 용역" || notices[0].Amount != 1000000 || notices[0].Category != model.CategoryConstruction {
		t.Fatalf("mapped notice = %+v", notices[0])
	}
	if got, want := strings.Join(pages, ","), "1,2"; got != want {
		t.Fatalf("pages = %s, want %s", got, want)
	}
}

func TestListDecodesOfficialServiceError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"30","resultMsg":"SERVICE KEY IS NOT REGISTERED ERROR."}}}`))
	}))
	defer server.Close()

	_, err := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client()}).List(context.Background(), model.CategoryGoods, ListQuery{})
	var serviceError *ServiceError
	if !errors.As(err, &serviceError) || serviceError.Code != "30" {
		t.Fatalf("error = %v, want decoded service error", err)
	}
}

func TestListRetriesBoundedTransientFailures(t *testing.T) {
	page, err := os.ReadFile("testdata/sanitized-construction-page.json")
	if err != nil {
		t.Fatal(err)
	}
	page = []byte(strings.Replace(string(page), `"totalCount": 2`, `"totalCount": 1`, 1))
	attempts := 0
	var delays []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(page)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client(), RetryAttempts: 3, RetryBaseDelay: time.Millisecond, RetryMaxDelay: time.Millisecond, Wait: func(ctx context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }})
	notices, err := client.List(context.Background(), model.CategoryConstruction, ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := attempts, 3; got != want {
		t.Fatalf("attempts = %d, want %d", got, want)
	}
	if got, want := len(delays), 2; got != want {
		t.Fatalf("retry delays = %d, want %d", got, want)
	}
	if got, want := len(notices), 1; got != want {
		t.Fatalf("notice count = %d, want %d", got, want)
	}
}

func TestOperationForMapsAllSupportedCategories(t *testing.T) {
	paths := map[model.Category]string{
		model.CategoryConstruction: "getBidPblancListInfoCnstwk",
		model.CategoryService:      "getBidPblancListInfoServc",
		model.CategoryGoods:        "getBidPblancListInfoThng",
		model.CategoryForeign:      "getBidPblancListInfoFrgcpt",
	}
	for category, want := range paths {
		operation, ok := operationFor(category)
		if !ok || operation.path != want {
			t.Fatalf("operationFor(%q) = %+v, %t; want %q", category, operation, ok, want)
		}
	}
}
