package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type HealthChecker interface {
	Ping(context.Context) error
}

func NewHTTPHandler(ui http.Handler, checker HealthChecker) http.Handler {
	if ui == nil {
		ui = http.NotFoundHandler()
	}
	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			if !healthMethod(w, r) {
				return
			}
			writeStatusJSON(w, http.StatusOK, "ok")
		case "/readyz":
			if !healthMethod(w, r) {
				return
			}
			if checker == nil {
				writeStatusJSON(w, http.StatusServiceUnavailable, "unavailable")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := checker.Ping(ctx); err != nil {
				writeStatusJSON(w, http.StatusServiceUnavailable, "unavailable")
				return
			}
			writeStatusJSON(w, http.StatusOK, "ready")
		default:
			ui.ServeHTTP(w, r)
		}
	}))
}

func healthMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	return false
}

func writeStatusJSON(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
