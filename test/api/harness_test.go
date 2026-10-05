// Package api_test drives the service end to end through its public APIs,
// gRPC and REST, with the in-memory store behind them. The gRPC server runs on
// an in-process connection; the REST gateway forwards to it, as in production.
package api_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/memstore"
	"github.com/adamsjoe/results-service/internal/service"
	"github.com/adamsjoe/results-service/internal/transport"
)

type servers struct {
	grpc    resultsv1.ResultsServiceClient
	restURL string
}

// startServers runs the gRPC server and REST gateway over store.
func startServers(t testing.TB, store service.Store) servers {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := transport.NewServer(service.New(store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	gw, err := transport.NewGateway(context.Background(), conn)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	httpSrv := httptest.NewServer(gw)
	t.Cleanup(httpSrv.Close)

	return servers{grpc: resultsv1.NewResultsServiceClient(conn), restURL: httpSrv.URL}
}

// newClient returns a gRPC client for gRPC-only tests.
func newClient(t *testing.T) resultsv1.ResultsServiceClient {
	t.Helper()
	return startServers(t, memstore.New()).grpc
}

func newClientWithStore(t *testing.T, store service.Store) resultsv1.ResultsServiceClient {
	t.Helper()
	return startServers(t, store).grpc
}
