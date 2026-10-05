//go:build integration

// Package reliability_test checks how the service behaves when things go
// wrong: the database disappears, a client gives up mid-write, or the server
// is told to stop while requests are in flight. It runs against real Postgres.
package reliability_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/service"
	"github.com/adamsjoe/results-service/internal/store"
	"github.com/adamsjoe/results-service/internal/testpg"
	"github.com/adamsjoe/results-service/internal/transport"
)

var db *testpg.Database

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	var err error
	if db, err = testpg.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start test database:", err)
		return 1
	}
	defer db.Close()
	if _, err := store.Migrate(ctx, db.URL); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	return m.Run()
}

// startServers runs the gRPC server and REST gateway in-process over the
// Postgres store, and returns a gRPC client and the REST base URL.
func startServers(t *testing.T) (resultsv1.ResultsServiceClient, string) {
	t.Helper()
	ctx := context.Background()

	pg, err := store.NewPostgres(ctx, db.URL)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(pg.Close)

	lis := bufconn.Listen(1 << 20)
	srv := transport.NewServer(service.New(pg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	gw, err := transport.NewGateway(ctx, conn)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	httpSrv := httptest.NewServer(gw)
	t.Cleanup(httpSrv.Close)

	return resultsv1.NewResultsServiceClient(conn), httpSrv.URL
}

func createRun(t *testing.T, c resultsv1.ResultsServiceClient) string {
	t.Helper()
	resp, err := c.CreateRun(context.Background(), &resultsv1.CreateRunRequest{Suite: "s", Branch: "main", CommitSha: "abc"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return resp.GetRun().GetId()
}

func passed(name string) *resultsv1.TestResult {
	return &resultsv1.TestResult{Name: name, Status: resultsv1.Status_STATUS_PASSED, DurationMs: 1}
}

// lockResults blocks every write to the results table until the returned
// function is called, so a request can be held in flight deliberately.
func lockResults(t *testing.T) (release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE results IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	done := false
	release = func() {
		if !done {
			done = true
			_ = tx.Rollback(ctx)
			_ = conn.Close(ctx)
		}
	}
	t.Cleanup(release)
	return release
}

// waitForBlockedWrite waits until some query is queued behind the lock.
func waitForBlockedWrite(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE NOT granted AND relation = 'results'::regclass").Scan(&waiting)
		if err != nil {
			t.Fatalf("query pg_locks: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no write became blocked on the results table")
}

func TestDatabaseOutage_ReturnsUnavailableThenRecovers(t *testing.T) {
	c, restURL := startServers(t)
	id := createRun(t, c)
	if _, err := c.RecordResults(context.Background(), &resultsv1.RecordResultsRequest{RunId: id, Results: []*resultsv1.TestResult{passed("a")}}); err != nil {
		t.Fatalf("RecordResults: %v", err)
	}

	ctx := context.Background()
	if err := db.Disconnect(ctx); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	t.Cleanup(func() { _ = db.Reconnect(context.Background()) })

	// During the outage: gRPC says Unavailable, REST says 503, nothing hangs.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.GetRun(callCtx, &resultsv1.GetRunRequest{RunId: id})
	if code := status.Code(err); code != codes.Unavailable {
		t.Errorf("GetRun during outage: expected Unavailable, got %v (%v)", code, err)
	}
	_, err = c.CreateRun(callCtx, &resultsv1.CreateRunRequest{Suite: "s", Branch: "main", CommitSha: "abc"})
	if code := status.Code(err); code != codes.Unavailable {
		t.Errorf("CreateRun during outage: expected Unavailable, got %v (%v)", code, err)
	}
	resp, err := http.Get(restURL + "/v1/runs/" + id)
	if err != nil {
		t.Fatalf("REST during outage: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("REST during outage: expected 503, got %d", resp.StatusCode)
	}

	// After the outage: the same server recovers without a restart.
	if err := db.Reconnect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	var run *resultsv1.Run
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		got, err := c.GetRun(attempt, &resultsv1.GetRunRequest{RunId: id})
		cancel()
		if err == nil {
			run = got.GetRun()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if run == nil {
		t.Fatal("service did not recover after the database came back")
	}
	if run.GetPassed() != 1 {
		t.Errorf("expected data to survive the outage, got %d passed", run.GetPassed())
	}
}

func TestDeadline_LeavesNoPartialWrite(t *testing.T) {
	c, _ := startServers(t)
	id := createRun(t, c)

	release := lockResults(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := c.RecordResults(ctx, &resultsv1.RecordResultsRequest{
		RunId:   id,
		Results: []*resultsv1.TestResult{passed("a"), passed("b"), passed("c")},
	})
	if code := status.Code(err); code != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v (%v)", code, err)
	}
	release()

	got, err := c.GetRun(context.Background(), &resultsv1.GetRunRequest{RunId: id})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.GetRun().GetPassed() != 0 {
		t.Errorf("expected no results stored after the deadline, got %d", got.GetRun().GetPassed())
	}
}

// buildServer compiles the real server binary for black-box tests.
// -buildvcs=false skips stamping Git details into the binary: inside the
// `make test` container the repo is owned by a different user, so Git
// refuses to read it and the build would fail.
func buildServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "server")
	out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "github.com/adamsjoe/results-service/cmd/server").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func TestGracefulShutdown_FinishesInFlightRequest(t *testing.T) {
	bin := buildServer(t)
	grpcAddr, httpAddr := freeAddr(t), freeAddr(t)

	var logs bytes.Buffer
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+db.URL, "GRPC_ADDR="+grpcAddr, "HTTP_ADDR="+httpAddr)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if t.Failed() {
			t.Logf("server logs:\n%s", logs.String())
		}
	})

	// Wait until the server is serving.
	healthy := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if resp, err := http.Get("http://" + httpAddr + "/healthz"); err == nil {
			_ = resp.Body.Close()
			healthy = resp.StatusCode == http.StatusOK
			if healthy {
				break
			}
		}
	}
	if !healthy {
		t.Fatal("server never became healthy")
	}

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	c := resultsv1.NewResultsServiceClient(conn)
	id := createRun(t, c)

	// Hold a write in flight, then ask the server to stop.
	release := lockResults(t)
	type outcome struct {
		resp *resultsv1.RecordResultsResponse
		err  error
	}
	inFlight := make(chan outcome, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := c.RecordResults(ctx, &resultsv1.RecordResultsRequest{RunId: id, Results: []*resultsv1.TestResult{passed("a")}})
		inFlight <- outcome{resp, err}
	}()
	waitForBlockedWrite(t)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	// The server must wait for the in-flight write rather than exit...
	select {
	case err := <-exited:
		t.Fatalf("server exited with a request still in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// ...while already refusing new HTTP traffic.
	if resp, err := http.Get("http://" + httpAddr + "/healthz"); err == nil {
		_ = resp.Body.Close()
		t.Error("expected the HTTP server to stop accepting requests after SIGTERM")
	}

	release()

	got := <-inFlight
	if got.err != nil {
		t.Fatalf("in-flight request failed: %v", got.err)
	}
	if got.resp.GetAccepted() != 1 {
		t.Errorf("expected the in-flight result to be stored, got %v", got.resp)
	}

	select {
	case err := <-exited:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Errorf("server exited with code %d", exitErr.ExitCode())
		} else if err != nil {
			t.Errorf("server exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not exit after the in-flight request finished")
	}
}
