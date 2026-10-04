// Command evalcatalog previews the static inventory without business services.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/evalcatalog"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8092", "preview listen address")
	flag.Parse()
	srv := &http.Server{
		Addr:              *listen,
		Handler:           evalcatalog.NewHandler(nil),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("evaluation catalog: http://%s/api/evals", *listen)
	log.Fatal(srv.ListenAndServe())
}
