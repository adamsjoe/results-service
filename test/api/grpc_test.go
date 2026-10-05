package api_test

// gRPC-only behaviour: client streaming (not exposed over REST), deadlines,
// and how unexpected errors are reported.

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/memstore"
	"github.com/adamsjoe/results-service/internal/service"
)

func mustCreateRun(t *testing.T, c resultsv1.ResultsServiceClient, suite string) *resultsv1.Run {
	t.Helper()
	resp, err := c.CreateRun(context.Background(), &resultsv1.CreateRunRequest{Suite: suite, Branch: "main", CommitSha: "abc123"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return resp.GetRun()
}

func mustGetRun(t *testing.T, c resultsv1.ResultsServiceClient, id string) *resultsv1.Run {
	t.Helper()
	resp, err := c.GetRun(context.Background(), &resultsv1.GetRunRequest{RunId: id})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return resp.GetRun()
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("expected %v, got %v (%v)", want, got, err)
	}
}

// ---- StreamResults -----------------------------------------------------

func TestStreamResults_StoresEveryMessage(t *testing.T) {
	c := newClient(t)
	run := mustCreateRun(t, c, "suite")

	stream, err := c.StreamResults(context.Background())
	if err != nil {
		t.Fatalf("StreamResults: %v", err)
	}
	for _, r := range []*resultsv1.TestResult{
		result("a", resultsv1.Status_STATUS_PASSED),
		result("b", resultsv1.Status_STATUS_PASSED),
		result("c", resultsv1.Status_STATUS_FAILED),
	} {
		if err := stream.Send(&resultsv1.StreamResultsRequest{RunId: run.GetId(), Result: r}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	if resp.GetAccepted() != 3 || resp.GetRejected() != 0 {
		t.Errorf("expected 3 accepted, got %v", resp)
	}

	got := mustGetRun(t, c, run.GetId())
	if got.GetPassed() != 2 || got.GetFailed() != 1 {
		t.Errorf("expected 2 passed and 1 failed, got %v", got)
	}
}

func TestStreamResults_MixedRunIDsStoreNothing(t *testing.T) {
	c := newClient(t)
	a, b := mustCreateRun(t, c, "suite"), mustCreateRun(t, c, "suite")

	stream, err := c.StreamResults(context.Background())
	if err != nil {
		t.Fatalf("StreamResults: %v", err)
	}
	_ = stream.Send(&resultsv1.StreamResultsRequest{RunId: a.GetId(), Result: result("x", resultsv1.Status_STATUS_PASSED)})
	_ = stream.Send(&resultsv1.StreamResultsRequest{RunId: b.GetId(), Result: result("y", resultsv1.Status_STATUS_PASSED)})
	_, err = stream.CloseAndRecv()
	wantCode(t, err, codes.InvalidArgument)

	if got := mustGetRun(t, c, a.GetId()); got.GetPassed() != 0 {
		t.Errorf("expected nothing stored for run a, got %d passed", got.GetPassed())
	}
}

func TestStreamResults_EmptyStreamIsInvalidArgument(t *testing.T) {
	c := newClient(t)
	stream, err := c.StreamResults(context.Background())
	if err != nil {
		t.Fatalf("StreamResults: %v", err)
	}
	_, err = stream.CloseAndRecv()
	wantCode(t, err, codes.InvalidArgument)
}

func TestStreamResults_UnknownRunIsNotFound(t *testing.T) {
	c := newClient(t)
	stream, err := c.StreamResults(context.Background())
	if err != nil {
		t.Fatalf("StreamResults: %v", err)
	}
	_ = stream.Send(&resultsv1.StreamResultsRequest{
		RunId:  "00000000-0000-4000-8000-000000000000",
		Result: result("a", resultsv1.Status_STATUS_PASSED),
	})
	_, err = stream.CloseAndRecv()
	wantCode(t, err, codes.NotFound)
}

func TestStreamResults_CancelledStreamStoresNothing(t *testing.T) {
	c := newClient(t)
	run := mustCreateRun(t, c, "suite")

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.StreamResults(ctx)
	if err != nil {
		t.Fatalf("StreamResults: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if err := stream.Send(&resultsv1.StreamResultsRequest{RunId: run.GetId(), Result: result(name, resultsv1.Status_STATUS_PASSED)}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	cancel() // abandon the upload before closing it

	_, err = stream.CloseAndRecv()
	wantCode(t, err, codes.Canceled)

	if got := mustGetRun(t, c, run.GetId()); got.GetPassed() != 0 {
		t.Errorf("expected nothing stored from a cancelled stream, got %d passed", got.GetPassed())
	}
}

// ---- Failure handling --------------------------------------------------

// brokenStore fails every call with an error that must not reach clients.
type brokenStore struct{ *memstore.Store }

func (brokenStore) GetRun(context.Context, string) (service.Run, error) {
	return service.Run{}, errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user results")
}

func TestUnexpectedErrorIsInternalWithoutDetails(t *testing.T) {
	c := newClientWithStore(t, brokenStore{memstore.New()})
	_, err := c.GetRun(context.Background(), &resultsv1.GetRunRequest{RunId: "any"})

	st, _ := status.FromError(err)
	if st.Code() != codes.Internal || st.Message() != "internal error" {
		t.Errorf("expected Internal with no details, got %v: %q", st.Code(), st.Message())
	}
}

// slowStore never answers GetRun until the caller gives up.
type slowStore struct{ *memstore.Store }

func (slowStore) GetRun(ctx context.Context, _ string) (service.Run, error) {
	<-ctx.Done()
	return service.Run{}, ctx.Err()
}

func TestDeadlineIsDeadlineExceeded(t *testing.T) {
	c := newClientWithStore(t, slowStore{memstore.New()})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.GetRun(ctx, &resultsv1.GetRunRequest{RunId: "any"})
	wantCode(t, err, codes.DeadlineExceeded)
}

func TestGetRun_EmptyIDIsInvalidArgument(t *testing.T) {
	c := newClient(t)
	_, err := c.GetRun(context.Background(), &resultsv1.GetRunRequest{})
	wantCode(t, err, codes.InvalidArgument)
}
