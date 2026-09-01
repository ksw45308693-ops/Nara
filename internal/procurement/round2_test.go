package procurement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfficialBaseURLLiteral(t *testing.T) {
	const official = "https://apis.data.go.kr/1230000/ad/BidPublicInfoService"
	if OfficialBaseURL != official {
		t.Fatalf("base URL = %q", OfficialBaseURL)
	}
}

func TestListReadsStringTotalCountAndRegionLookupCaches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/getBidPblancListInfoPrtcptPsblRgn" {
			_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":{"prtcptPsblRgnNm":"서울"}},"totalCount":"1"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[]},"totalCount":"0"}}}`))
	}))
	defer server.Close()
	c := NewClient(Config{BaseURL: server.URL})
	if _, err := c.List(context.Background(), "goods", ListQuery{}); err != nil {
		t.Fatal(err)
	}
	first, err := c.LookupRegion(context.Background(), "N-1", "00")
	if err != nil || first != "서울" {
		t.Fatalf("region=%q err=%v", first, err)
	}
	if _, err := c.LookupRegion(context.Background(), "N-1", "00"); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
