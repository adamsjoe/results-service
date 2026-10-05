// Command server is the results-service binary.
//
// Subcommands:
//
//	serve        start the HTTP server (default)
//	healthcheck  call /healthz and exit 0 if healthy, 1 if not
//	migrate      run database migrations (stub until milestone 4)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "serve":
		err = serve(logger)
	case "healthcheck":
		err = healthcheck()
	case "migrate":
		logger.Info("migrate: no migrations yet")
	default:
		err = fmt.Errorf("unknown command %q (want serve, healthcheck or migrate)", cmd)
	}

	if err != nil {
		logger.Error("exiting", "command", cmd, "error", err)
		os.Exit(1)
	}
}

// httpAddr returns the listen address from HTTP_ADDR, defaulting to :8080.
func httpAddr() string {
	if addr := os.Getenv("HTTP_ADDR"); addr != "" {
		return addr
	}
	return ":8080"
}

// serve runs the HTTP server until SIGINT or SIGTERM, then shuts down gracefully.
func serve(logger *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:              httpAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err // server failed to start, e.g. port in use
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// healthcheck calls the local /healthz endpoint. The distroless image has no
// shell or curl, so Docker's healthcheck runs this subcommand instead.
func healthcheck() error {
	addr := httpAddr()
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
