package procurement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupRegionSendsRequiredParametersAndCombinesUniqueRegions(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"prtcptPsblRgnNm":" 서울 "},{"prtcptPsblRgnNm":"부산"},{"prtcptPsblRgnNm":"서울"},{"prtcptPsblRgnNm":" "}]},"totalCount":4}}}`))
	}))
	defer server.Close()

	region, err := NewClient(Config{BaseURL: server.URL, ServiceKey: "raw+/="}).LookupRegion(context.Background(), "N-1", "02")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := region, "부산, 서울"; got != want {
		t.Fatalf("region = %q, want %q", got, want)
	}
	if got, want := gotPath, "/getBidPblancListInfoPrtcptPsblRgn"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	for key, want := range map[string]string{
		"serviceKey": "raw+/=", "type": "json", "inqryDiv": "1", "pageNo": "1", "bidNtceNo": "N-1", "bidNtceOrd": "02",
	} {
		if got := gotQuery.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q; full query = %v", key, got, want, gotQuery)
		}
	}
	rows, err := strconv.Atoi(gotQuery.Get("numOfRows"))
	if err != nil || rows < 1 || rows > maxPageSize {
		t.Fatalf("numOfRows = %q, want 1..%d", gotQuery.Get("numOfRows"), maxPageSize)
	}
}

func TestLookupRegionCacheSeparatesBidSequence(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := r.URL.Query().Get("bidNtceOrd")
		mu.Lock()
		calls[sequence]++
		mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":{"prtcptPsblRgnNm":%q}},"totalCount":1}}}`, sequence)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})
	for _, tc := range []struct{ sequence, want string }{{"00", "00"}, {"01", "01"}, {"00", "00"}} {
		got, err := client.LookupRegion(context.Background(), "N-1", tc.sequence)
		if err != nil || got != tc.want {
			t.Fatalf("LookupRegion(%q) = %q, %v; want %q", tc.sequence, got, err, tc.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["00"] != 1 || calls["01"] != 1 {
		t.Fatalf("calls = %v, want one request per sequence", calls)
	}
}

func TestLookupRegionCachesEmptyResult(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[]},"totalCount":0}}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})
	for range 2 {
		region, err := client.LookupRegion(context.Background(), "N-empty", "00")
		if err != nil || region != "" {
			t.Fatalf("region = %q, err = %v; want cached empty result", region, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestLookupRegionCoalescesConcurrentMisses(t *testing.T) {
	const workers = 8
	start := make(chan struct{})
	release := make(chan struct{})
	entered := make(chan struct{}, workers)
	var attempts atomic.Int32
	transport := lookupRegionRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		entered <- struct{}{}
		<-release
		return lookupRegionJSONResponse(req, `{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":{"prtcptPsblRgnNm":"서울"}},"totalCount":1}}}`), nil
	})
	client := NewClient(Config{HTTPClient: &http.Client{Transport: transport}})
	type result struct {
		region string
		err    error
	}
	results := make(chan result, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			region, err := client.LookupRegion(context.Background(), "N-shared", "00")
			results <- result{region: region, err: err}
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("lookup request did not start")
	}
	var duplicate bool
	select {
	case <-entered:
		duplicate = true
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	for range workers {
		got := <-results
		if got.err != nil || got.region != "서울" {
			t.Fatalf("region = %q, error = %T %v", got.region, got.err, got.err)
		}
	}
	if duplicate || attempts.Load() != 1 {
		t.Fatalf("transport attempts = %d, want one coalesced miss", attempts.Load())
	}
}

func TestLookupRegionRetriesHTTPStatusAndClampsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	var delay time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":{"prtcptPsblRgnNm":"서울"}},"totalCount":1}}}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL, RetryAttempts: 2, RetryBaseDelay: time.Millisecond, RetryMaxDelay: 10 * time.Millisecond,
		Wait: func(ctx context.Context, got time.Duration) error { delay = got; return nil },
	})
	region, err := client.LookupRegion(context.Background(), "N-retry", "00")
	if err != nil || region != "서울" {
		t.Fatalf("region = %q, err = %v", region, err)
	}
	if attempts.Load() != 2 || delay != 10*time.Millisecond {
		t.Fatalf("attempts = %d, retry delay = %s; want 2 and 10ms", attempts.Load(), delay)
	}
}

func TestLookupRegionRejectsNilEnvelopeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":null}}`))
	}))
	defer server.Close()

	_, err := NewClient(Config{BaseURL: server.URL}).LookupRegion(context.Background(), "N-nil", "00")
	var malformed *MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("error = %v, want MalformedResponseError", err)
	}
}

func TestLookupRegionRejectsEncodedServiceKey(t *testing.T) {
	client := NewClient(Config{ServiceKey: "already%2Fencoded", HTTPClient: &http.Client{Transport: lookupRegionRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("encoded key must be rejected before transport")
		return nil, nil
	})}})
	_, err := client.LookupRegion(context.Background(), "N-key", "00")
	if err != ErrEncodedServiceKey {
		t.Fatalf("error = %v, want ErrEncodedServiceKey", err)
	}
}

func TestLookupRegionBudgetIsTypedCachedAndPerClient(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":{"prtcptPsblRgnNm":"서울"}},"totalCount":1}}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, LookupBudget: 1})
	for range 2 {
		if region, err := client.LookupRegion(context.Background(), "N-cached", "00"); err != nil || region != "서울" {
			t.Fatalf("cached lookup = %q, %v", region, err)
		}
	}
	_, err := client.LookupRegion(context.Background(), "N-exhausted", "00")
	var exhausted *LookupBudgetError
	if !errors.As(err, &exhausted) || !errors.Is(err, ErrLookupBudget) {
		t.Fatalf("error = %v, want typed lookup-budget exhaustion", err)
	}
	if exhausted.Limit != 1 || exhausted.Used != 1 || exhausted.BidNumber != "N-exhausted" || exhausted.BidSequence != "00" {
		t.Fatalf("budget error = %+v", exhausted)
	}

	other := NewClient(Config{BaseURL: server.URL, LookupBudget: 1})
	if _, err := other.LookupRegion(context.Background(), "N-other-client", "00"); err != nil {
		t.Fatalf("another client's budget was shared: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("transport calls = %d, want 2", got)
	}
}

type lookupRegionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f lookupRegionRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func lookupRegionJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
