// Package api_test drives the service end to end through its public API, over
// an in-process connection, with the in-memory store behind it.
package api_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/memstore"
	"github.com/adamsjoe/results-service/internal/service"
	"github.com/adamsjoe/results-service/internal/transport"
)

// newClient starts the gRPC server on an in-memory listener backed by an
// empty in-memory store, and returns a client connected to it.
func newClient(t *testing.T) resultsv1.ResultsServiceClient {
	t.Helper()
	return newClientWithStore(t, memstore.New())
}

func newClientWithStore(t *testing.T, store service.Store) resultsv1.ResultsServiceClient {
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

	return resultsv1.NewResultsServiceClient(conn)
}
