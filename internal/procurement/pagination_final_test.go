package procurement

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"g2b-monitor/internal/model"
)

func TestListReturnsTypedIncompletePageErrorWithoutPartialNotices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"bidNtceNo":"N-1","bidNtceOrd":"00","bidNtceNm":"first"}]},"totalCount":2}}}`))
	}))
	defer server.Close()

	notices, err := NewClient(Config{BaseURL: server.URL}).List(context.Background(), model.CategoryGoods, ListQuery{PageSize: 2})
	var incomplete *IncompletePageError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want IncompletePageError", err)
	}
	if notices != nil {
		t.Fatalf("partial notices = %+v, want nil", notices)
	}
	if incomplete.Page != 1 || incomplete.Expected != 2 || incomplete.Received != 1 || incomplete.TotalCount != 2 {
		t.Fatalf("incomplete page = %+v", incomplete)
	}
}

func TestListReturnsTypedRepeatedPageErrorWithoutPartialNotices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"bidNtceNo":"N-repeat","bidNtceOrd":"00","bidNtceNm":"same"}]},"totalCount":2}}}`))
	}))
	defer server.Close()

	notices, err := NewClient(Config{BaseURL: server.URL}).List(context.Background(), model.CategoryGoods, ListQuery{PageSize: 1})
	var repeated *RepeatedPageError
	if !errors.As(err, &repeated) {
		t.Fatalf("error = %v, want RepeatedPageError", err)
	}
	if notices != nil {
		t.Fatalf("partial notices = %+v, want nil", notices)
	}
	if repeated.Page != 2 || repeated.FirstPage != 1 {
		t.Fatalf("repeated page = %+v", repeated)
	}
}

func TestListReturnsTypedMaxPageErrorWithoutPartialNotices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"bidNtceNo":"N-1","bidNtceOrd":"00","bidNtceNm":"first"}]},"totalCount":2}}}`))
	}))
	defer server.Close()

	notices, err := NewClient(Config{BaseURL: server.URL, MaxPages: 1}).List(context.Background(), model.CategoryGoods, ListQuery{PageSize: 1})
	var maximum *MaxPageError
	if !errors.As(err, &maximum) {
		t.Fatalf("error = %v, want MaxPageError", err)
	}
	if notices != nil {
		t.Fatalf("partial notices = %+v, want nil", notices)
	}
	if maximum.Page != 2 || maximum.MaxPages != 1 || maximum.TotalCount != 2 {
		t.Fatalf("max page = %+v", maximum)
	}
}
