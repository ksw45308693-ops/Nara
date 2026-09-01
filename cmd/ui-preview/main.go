// Command ui-preview serves the read-only sample interface for local visual QA.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
	_ "time/tzdata"

	appweb "g2b-monitor/internal/web"
)

func main() {
	address := flag.String("listen", "127.0.0.1:18080", "preview listen address")
	flag.Parse()
	server := &http.Server{
		Addr: *address, Handler: appweb.NewHandler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute,
	}
	log.Printf("read-only UI preview: http://%s/dashboard", *address)
	log.Fatal(server.ListenAndServe())
}
