package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type healthStub struct{ err error }

func (h healthStub) Ping(context.Context) error { return h.err }

func TestHTTPHandlerHealthReadinessAndSecurityHeaders(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ui:" + r.URL.Path))
	})
	handler := NewHTTPHandler(ui, healthStub{})

	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s status=%d content-type=%q body=%q", path, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
		if recorder.Header().Get("X-Content-Type-Options") != "nosniff" || recorder.Header().Get("Referrer-Policy") == "" {
			t.Fatalf("%s security headers = %v", path, recorder.Header())
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ui:/dashboard" {
		t.Fatalf("UI delegation status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPHandlerReadinessFailsClosed(t *testing.T) {
	handler := NewHTTPHandler(http.NotFoundHandler(), healthStub{err: errors.New("database unavailable")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "database unavailable") {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
