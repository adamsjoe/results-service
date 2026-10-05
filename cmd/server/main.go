// Command server is the results-service binary.
//
// Subcommands:
//
//	serve        start the gRPC and HTTP servers (default)
//	healthcheck  call /healthz and exit 0 if healthy, 1 if not
//	migrate      apply database migrations from DATABASE_URL
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/adamsjoe/results-service/internal/service"
	"github.com/adamsjoe/results-service/internal/store"
	"github.com/adamsjoe/results-service/internal/transport"
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
		err = migrate(logger)
	default:
		err = fmt.Errorf("unknown command %q (want serve, healthcheck or migrate)", cmd)
	}

	if err != nil {
		logger.Error("exiting", "command", cmd, "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func httpAddr() string { return env("HTTP_ADDR", ":8080") }
func grpcAddr() string { return env("GRPC_ADDR", ":9090") }

// serve runs the gRPC server and the HTTP health endpoint until SIGINT or
// SIGTERM, then shuts both down gracefully.
func serve(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pg, err := store.NewPostgres(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pg.Close()

	grpcServer := transport.NewServer(service.New(pg), logger)
	grpcListener, err := net.Listen("tcp", grpcAddr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", grpcAddr(), err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	httpServer := &http.Server{
		Addr:              httpAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("grpc server listening", "addr", grpcAddr())
		if err := grpcServer.Serve(grpcListener); err != nil {
			errCh <- fmt.Errorf("grpc server: %w", err)
		}
	}()
	go func() {
		logger.Info("http server listening", "addr", httpAddr())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		grpcServer.Stop()
		_ = httpServer.Close()
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop() // waits for in-flight calls
		close(stopped)
	}()
	httpErr := httpServer.Shutdown(shutdownCtx)

	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcServer.Stop() // in-flight calls took too long
	}
	return httpErr
}

// migrate applies any pending migrations to the database at DATABASE_URL.
func migrate(logger *slog.Logger) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	applied, err := store.Migrate(ctx, url)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		logger.Info("database is up to date")
	} else {
		logger.Info("migrations applied", "versions", applied)
	}
	return nil
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
