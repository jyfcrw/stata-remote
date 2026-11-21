package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"stata-remote/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	executable := flag.String("stata", "", "path to the installed Windows Stata executable")
	workspace := flag.String("workspace", "", "Windows project directory to run and share")
	listen := flag.String("listen", "127.0.0.1:8765", "HTTP listen address")
	cert := flag.String("tls-cert", "", "optional TLS certificate file")
	key := flag.String("tls-key", "", "optional TLS private key file")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if runtime.GOOS != "windows" {
		return errors.New("the Stata Remote server runs on Windows")
	}
	if (*cert == "") != (*key == "") {
		return errors.New("set both --tls-cert and --tls-key to enable HTTPS")
	}
	app, err := server.New(*executable, *workspace, os.Getenv("STATA_REMOTE_TOKEN"))
	if err != nil {
		return err
	}
	defer app.Close()
	httpServer := &http.Server{Addr: *listen, Handler: app, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("Stata Remote listening on %s", *listen)
	if *cert != "" {
		err = httpServer.ListenAndServeTLS(*cert, *key)
	} else {
		err = httpServer.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("run HTTP server: %w", err)
}
