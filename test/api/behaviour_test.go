package api_test

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/service"
)

// Behaviours shared by both protocols. Each test runs once over gRPC and once
// over REST, against fresh servers.

const unknownRunID = "00000000-0000-4000-8000-000000000000"

func newRun(suite string) *resultsv1.CreateRunRequest {
	return &resultsv1.CreateRunRequest{Suite: suite, Branch: "main", CommitSha: "abc123"}
}

func result(name string, s resultsv1.Status) *resultsv1.TestResult {
	return &resultsv1.TestResult{Name: name, Status: s, DurationMs: 5}
}

func mustCreate(t *testing.T, a api, suite string) *resultsv1.Run {
	t.Helper()
	run, o := a.createRun(t, newRun(suite))
	if !a.isOK(o) {
		t.Fatalf("createRun: %+v", o)
	}
	return run
}

func mustGet(t *testing.T, a api, id string) *resultsv1.Run {
	t.Helper()
	run, o := a.getRun(t, id)
	if !a.isOK(o) {
		t.Fatalf("getRun: %+v", o)
	}
	return run
}

func expect(t *testing.T, a api, o outcome, wantGRPC codes.Code, wantHTTP int) {
	t.Helper()
	if !a.matches(o, wantGRPC, wantHTTP) {
		t.Fatalf("%s: expected gRPC %v / HTTP %d, got %+v", a.name(), wantGRPC, wantHTTP, o)
	}
}

func TestCreateRun_ReturnsTheRun(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		run := mustCreate(t, a, "checkout-e2e")
		if run.GetId() == "" || run.GetSuite() != "checkout-e2e" || run.GetBranch() != "main" || run.GetCommitSha() != "abc123" {
			t.Errorf("unexpected run: %v", run)
		}
		if run.GetStartedAt().AsTime().IsZero() {
			t.Error("expected started_at to be set")
		}
	})
}

func TestCreateRun_MissingFieldIsInvalid(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		_, o := a.createRun(t, &resultsv1.CreateRunRequest{Branch: "main", CommitSha: "abc"})
		expect(t, a, o, codes.InvalidArgument, http.StatusBadRequest)
	})
}

func TestRecordResults_AcceptsValidAndRejectsInvalid(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		run := mustCreate(t, a, "suite")

		resp, o := a.recordResults(t, &resultsv1.RecordResultsRequest{
			RunId: run.GetId(),
			Results: []*resultsv1.TestResult{
				result("a", resultsv1.Status_STATUS_PASSED),
				result("b", resultsv1.Status_STATUS_FAILED),
				result("c", resultsv1.Status_STATUS_SKIPPED),
				result("d", resultsv1.Status_STATUS_UNSPECIFIED), // rejected
				result("", resultsv1.Status_STATUS_PASSED),       // rejected
			},
		})
		if !a.isOK(o) {
			t.Fatalf("recordResults: %+v", o)
		}
		if resp.GetAccepted() != 3 || resp.GetRejected() != 2 {
			t.Errorf("expected 3 accepted and 2 rejected, got %v", resp)
		}

		got := mustGet(t, a, run.GetId())
		if got.GetPassed() != 1 || got.GetFailed() != 1 || got.GetSkipped() != 1 {
			t.Errorf("expected one of each status, got %v", got)
		}
	})
}

func TestRecordResults_Errors(t *testing.T) {
	over := make([]*resultsv1.TestResult, service.MaxBatchSize+1)
	for i := range over {
		over[i] = result("t", resultsv1.Status_STATUS_PASSED)
	}
	one := []*resultsv1.TestResult{result("a", resultsv1.Status_STATUS_PASSED)}

	cases := []struct {
		name     string
		unknown  bool // use an unknown run instead of a real one
		results  []*resultsv1.TestResult
		wantGRPC codes.Code
		wantHTTP int
	}{
		{"unknown run", true, one, codes.NotFound, http.StatusNotFound},
		{"empty batch", false, nil, codes.InvalidArgument, http.StatusBadRequest},
		{"batch over limit", false, over, codes.InvalidArgument, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachAPI(t, func(t *testing.T, a api) {
				id := unknownRunID
				if !tc.unknown {
					id = mustCreate(t, a, "suite").GetId()
				}
				_, o := a.recordResults(t, &resultsv1.RecordResultsRequest{RunId: id, Results: tc.results})
				expect(t, a, o, tc.wantGRPC, tc.wantHTTP)
			})
		})
	}
}

func TestGetRun_UnknownIsNotFound(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		_, o := a.getRun(t, unknownRunID)
		expect(t, a, o, codes.NotFound, http.StatusNotFound)
	})
}

func TestGetRun_NewRunHasZeroCounts(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		run := mustGet(t, a, mustCreate(t, a, "suite").GetId())
		if run.GetPassed() != 0 || run.GetFailed() != 0 || run.GetSkipped() != 0 {
			t.Errorf("expected zero counts, got %v", run)
		}
	})
}

func TestListRuns_PagesNewestFirst(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		first, second, third := mustCreate(t, a, "s"), mustCreate(t, a, "s"), mustCreate(t, a, "s")

		page1, o := a.listRuns(t, &resultsv1.ListRunsRequest{PageSize: 2})
		if !a.isOK(o) {
			t.Fatalf("page 1: %+v", o)
		}
		if len(page1.GetRuns()) != 2 || page1.GetRuns()[0].GetId() != third.GetId() || page1.GetRuns()[1].GetId() != second.GetId() {
			t.Fatalf("page 1: expected [third, second], got %v", page1.GetRuns())
		}
		if page1.GetNextPageToken() == "" {
			t.Fatal("page 1: expected a next page token")
		}

		page2, o := a.listRuns(t, &resultsv1.ListRunsRequest{PageSize: 2, PageToken: page1.GetNextPageToken()})
		if !a.isOK(o) {
			t.Fatalf("page 2: %+v", o)
		}
		if len(page2.GetRuns()) != 1 || page2.GetRuns()[0].GetId() != first.GetId() {
			t.Errorf("page 2: expected [first], got %v", page2.GetRuns())
		}
		if page2.GetNextPageToken() != "" {
			t.Errorf("page 2: expected no next page token, got %q", page2.GetNextPageToken())
		}
	})
}

func TestListRuns_FiltersBySuite(t *testing.T) {
	forEachAPI(t, func(t *testing.T, a api) {
		mustCreate(t, a, "checkout")
		mustCreate(t, a, "search")

		resp, o := a.listRuns(t, &resultsv1.ListRunsRequest{Suite: "checkout"})
		if !a.isOK(o) {
			t.Fatalf("listRuns: %+v", o)
		}
		if len(resp.GetRuns()) != 1 || resp.GetRuns()[0].GetSuite() != "checkout" {
			t.Errorf("expected only the checkout run, got %v", resp.GetRuns())
		}
	})
}

func TestListRuns_BadInputIsInvalid(t *testing.T) {
	cases := []struct {
		name string
		req  *resultsv1.ListRunsRequest
	}{
		{"bad page token", &resultsv1.ListRunsRequest{PageToken: "not-a-token"}},
		{"negative page size", &resultsv1.ListRunsRequest{PageSize: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachAPI(t, func(t *testing.T, a api) {
				_, o := a.listRuns(t, tc.req)
				expect(t, a, o, codes.InvalidArgument, http.StatusBadRequest)
			})
		})
	}
}
