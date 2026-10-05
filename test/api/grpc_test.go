package api_test

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

func result(name string, s resultsv1.Status) *resultsv1.TestResult {
	return &resultsv1.TestResult{Name: name, Status: s, DurationMs: 5}
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("expected %v, got %v (%v)", want, got, err)
	}
}

// ---- CreateRun ---------------------------------------------------------

func TestCreateRun_ReturnsTheRun(t *testing.T) {
	c := newClient(t)
	run := mustCreateRun(t, c, "checkout-e2e")

	if run.GetId() == "" || run.GetSuite() != "checkout-e2e" || run.GetBranch() != "main" || run.GetCommitSha() != "abc123" {
		t.Errorf("unexpected run: %v", run)
	}
	if run.GetStartedAt() == nil || run.GetStartedAt().AsTime().IsZero() {
		t.Error("expected started_at to be set")
	}
}

func TestCreateRun_MissingFieldIsInvalidArgument(t *testing.T) {
	c := newClient(t)
	_, err := c.CreateRun(context.Background(), &resultsv1.CreateRunRequest{Branch: "main", CommitSha: "abc"})
	wantCode(t, err, codes.InvalidArgument)
}

// ---- RecordResults -----------------------------------------------------

func TestRecordResults_AcceptsValidAndRejectsInvalid(t *testing.T) {
	c := newClient(t)
	run := mustCreateRun(t, c, "suite")

	resp, err := c.RecordResults(context.Background(), &resultsv1.RecordResultsRequest{
		RunId: run.GetId(),
		Results: []*resultsv1.TestResult{
			result("a", resultsv1.Status_STATUS_PASSED),
			result("b", resultsv1.Status_STATUS_FAILED),
			result("c", resultsv1.Status_STATUS_SKIPPED),
			result("d", resultsv1.Status_STATUS_UNSPECIFIED), // rejected
			result("", resultsv1.Status_STATUS_PASSED),       // rejected
		},
	})
	if err != nil {
		t.Fatalf("RecordResults: %v", err)
	}
	if resp.GetAccepted() != 3 || resp.GetRejected() != 2 {
		t.Errorf("expected 3 accepted and 2 rejected, got %v", resp)
	}

	got := mustGetRun(t, c, run.GetId())
	if got.GetPassed() != 1 || got.GetFailed() != 1 || got.GetSkipped() != 1 {
		t.Errorf("expected one of each status, got %v", got)
	}
}

func TestRecordResults_Errors(t *testing.T) {
	over := make([]*resultsv1.TestResult, service.MaxBatchSize+1)
	for i := range over {
		over[i] = result("t", resultsv1.Status_STATUS_PASSED)
	}

	cases := []struct {
		name    string
		runID   func(run *resultsv1.Run) string
		results []*resultsv1.TestResult
		want    codes.Code
	}{
		{"unknown run", func(*resultsv1.Run) string { return "00000000-0000-4000-8000-000000000000" },
			[]*resultsv1.TestResult{result("a", resultsv1.Status_STATUS_PASSED)}, codes.NotFound},
		{"empty batch", func(r *resultsv1.Run) string { return r.GetId() }, nil, codes.InvalidArgument},
		{"batch over limit", func(r *resultsv1.Run) string { return r.GetId() }, over, codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t)
			run := mustCreateRun(t, c, "suite")
			_, err := c.RecordResults(context.Background(), &resultsv1.RecordResultsRequest{RunId: tc.runID(run), Results: tc.results})
			wantCode(t, err, tc.want)
		})
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

// ---- GetRun ------------------------------------------------------------

func TestGetRun_Errors(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want codes.Code
	}{
		{"empty id", "", codes.InvalidArgument},
		{"unknown id", "00000000-0000-4000-8000-000000000000", codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t)
			_, err := c.GetRun(context.Background(), &resultsv1.GetRunRequest{RunId: tc.id})
			wantCode(t, err, tc.want)
		})
	}
}

// ---- ListRuns ----------------------------------------------------------

func TestListRuns_PagesNewestFirst(t *testing.T) {
	c := newClient(t)
	first, second, third := mustCreateRun(t, c, "s"), mustCreateRun(t, c, "s"), mustCreateRun(t, c, "s")

	page1, err := c.ListRuns(context.Background(), &resultsv1.ListRunsRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListRuns page 1: %v", err)
	}
	if len(page1.GetRuns()) != 2 || page1.GetRuns()[0].GetId() != third.GetId() || page1.GetRuns()[1].GetId() != second.GetId() {
		t.Fatalf("page 1: expected [third, second], got %v", page1.GetRuns())
	}
	if page1.GetNextPageToken() == "" {
		t.Fatal("page 1: expected a next page token")
	}

	page2, err := c.ListRuns(context.Background(), &resultsv1.ListRunsRequest{PageSize: 2, PageToken: page1.GetNextPageToken()})
	if err != nil {
		t.Fatalf("ListRuns page 2: %v", err)
	}
	if len(page2.GetRuns()) != 1 || page2.GetRuns()[0].GetId() != first.GetId() {
		t.Errorf("page 2: expected [first], got %v", page2.GetRuns())
	}
	if page2.GetNextPageToken() != "" {
		t.Errorf("page 2: expected no next page token, got %q", page2.GetNextPageToken())
	}
}

func TestListRuns_BadTokenIsInvalidArgument(t *testing.T) {
	c := newClient(t)
	_, err := c.ListRuns(context.Background(), &resultsv1.ListRunsRequest{PageToken: "not-a-token"})
	wantCode(t, err, codes.InvalidArgument)
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
