package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeHTTPStopsCleanlyWithContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeHTTP(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ready"))
		}))
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "ready" {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeHTTP shutdown error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHTTP did not stop after cancellation")
	}
}
