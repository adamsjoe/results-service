// Package storetest is a contract test suite for service.Store.
//
// Every store implementation runs the same suite, so the in-memory store used
// in unit tests is held to exactly the behaviour of the Postgres store.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adamsjoe/results-service/internal/service"
)

// Factory returns an empty store for one test.
type Factory func(t *testing.T) service.Store

// unknownUUID is well formed but never issued.
const unknownUUID = "00000000-0000-4000-8000-000000000000"

// Run runs the contract suite against stores made by newStore.
func Run(t *testing.T, newStore Factory) {
	tests := []struct {
		name string
		fn   func(t *testing.T, s service.Store)
	}{
		{"CreateRun assigns ID and StartedAt", testCreateRunAssignsIDAndStartedAt},
		{"CreateRun IDs are unique", testCreateRunIDsAreUnique},
		{"GetRun returns stored fields with zero counts", testGetRunReturnsStoredFields},
		{"GetRun unknown run is not found", testGetRunUnknownIsNotFound},
		{"AddResults counts by status", testAddResultsCountsByStatus},
		{"AddResults accumulates across batches", testAddResultsAccumulates},
		{"AddResults empty batch for existing run succeeds", testAddResultsEmptyBatch},
		{"AddResults unknown run is not found", testAddResultsUnknownIsNotFound},
		{"ListRuns empty store returns no runs", testListRunsEmpty},
		{"ListRuns newest first", testListRunsNewestFirst},
		{"ListRuns filters by suite", testListRunsFiltersBySuite},
		{"ListRuns applies limit and offset", testListRunsLimitOffset},
		{"ListRuns includes counts", testListRunsIncludesCounts},
		{"Cancelled context returns an error", testCancelledContext},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStore(t))
		})
	}
}

var ctx = context.Background()

func mustCreate(t *testing.T, s service.Store, suite string) service.Run {
	t.Helper()
	run, err := s.CreateRun(ctx, suite, "main", "abc123")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func mustAdd(t *testing.T, s service.Store, runID string, results ...service.TestResult) {
	t.Helper()
	if err := s.AddResults(ctx, runID, results); err != nil {
		t.Fatalf("AddResults: %v", err)
	}
}

func result(name string, status service.Status) service.TestResult {
	r := service.TestResult{Name: name, Status: status, DurationMS: 5}
	if status == service.StatusFailed {
		r.ErrorMessage = "boom"
	}
	return r
}

func ids(runs []service.Run) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testCreateRunAssignsIDAndStartedAt(t *testing.T, s service.Store) {
	run := mustCreate(t, s, "checkout")
	if run.ID == "" {
		t.Error("expected an ID")
	}
	if age := time.Since(run.StartedAt); run.StartedAt.IsZero() || age < -time.Minute || age > time.Minute {
		t.Errorf("StartedAt %v is not close to now", run.StartedAt)
	}
	if run.Suite != "checkout" || run.Branch != "main" || run.CommitSHA != "abc123" {
		t.Errorf("fields not returned: %+v", run)
	}
}

func testCreateRunIDsAreUnique(t *testing.T, s service.Store) {
	a, b := mustCreate(t, s, "x"), mustCreate(t, s, "x")
	if a.ID == b.ID {
		t.Errorf("two runs share ID %s", a.ID)
	}
}

func testGetRunReturnsStoredFields(t *testing.T, s service.Store) {
	created := mustCreate(t, s, "checkout")

	got, err := s.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != created.ID || got.Suite != "checkout" || got.Branch != "main" || got.CommitSHA != "abc123" {
		t.Errorf("fields differ: got %+v, created %+v", got, created)
	}
	if !got.StartedAt.Equal(created.StartedAt) {
		t.Errorf("StartedAt differs: got %v, created %v", got.StartedAt, created.StartedAt)
	}
	if got.Passed != 0 || got.Failed != 0 || got.Skipped != 0 {
		t.Errorf("expected zero counts, got %+v", got)
	}
}

func testGetRunUnknownIsNotFound(t *testing.T, s service.Store) {
	for _, id := range []string{unknownUUID, "not-a-uuid"} {
		if _, err := s.GetRun(ctx, id); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("GetRun(%q): expected ErrNotFound, got %v", id, err)
		}
	}
}

func testAddResultsCountsByStatus(t *testing.T, s service.Store) {
	run := mustCreate(t, s, "x")
	mustAdd(t, s, run.ID,
		result("a", service.StatusPassed),
		result("b", service.StatusPassed),
		result("c", service.StatusFailed),
		result("d", service.StatusSkipped),
	)

	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Passed != 2 || got.Failed != 1 || got.Skipped != 1 {
		t.Errorf("expected 2/1/1, got passed=%d failed=%d skipped=%d", got.Passed, got.Failed, got.Skipped)
	}
}

func testAddResultsAccumulates(t *testing.T, s service.Store) {
	run := mustCreate(t, s, "x")
	mustAdd(t, s, run.ID, result("a", service.StatusPassed))
	mustAdd(t, s, run.ID, result("b", service.StatusPassed), result("c", service.StatusFailed))

	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Passed != 2 || got.Failed != 1 {
		t.Errorf("expected 2 passed and 1 failed across batches, got %+v", got)
	}
}

func testAddResultsEmptyBatch(t *testing.T, s service.Store) {
	run := mustCreate(t, s, "x")
	if err := s.AddResults(ctx, run.ID, nil); err != nil {
		t.Errorf("expected no error for an empty batch, got %v", err)
	}
}

func testAddResultsUnknownIsNotFound(t *testing.T, s service.Store) {
	batches := map[string][]service.TestResult{
		"empty batch":     nil,
		"non-empty batch": {result("a", service.StatusPassed)},
	}
	for name, batch := range batches {
		for _, id := range []string{unknownUUID, "not-a-uuid"} {
			if err := s.AddResults(ctx, id, batch); !errors.Is(err, service.ErrNotFound) {
				t.Errorf("%s, id %q: expected ErrNotFound, got %v", name, id, err)
			}
		}
	}
}

func testListRunsEmpty(t *testing.T, s service.Store) {
	runs, err := s.ListRuns(ctx, service.ListRunsQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs, got %d", len(runs))
	}
}

func testListRunsNewestFirst(t *testing.T, s service.Store) {
	a, b, c := mustCreate(t, s, "x"), mustCreate(t, s, "x"), mustCreate(t, s, "x")

	runs, err := s.ListRuns(ctx, service.ListRunsQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if want := []string{c.ID, b.ID, a.ID}; !equal(ids(runs), want) {
		t.Errorf("expected %v, got %v", want, ids(runs))
	}
}

func testListRunsFiltersBySuite(t *testing.T, s service.Store) {
	a := mustCreate(t, s, "checkout")
	mustCreate(t, s, "search")
	b := mustCreate(t, s, "checkout")

	runs, err := s.ListRuns(ctx, service.ListRunsQuery{Suite: "checkout", Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if want := []string{b.ID, a.ID}; !equal(ids(runs), want) {
		t.Errorf("expected %v, got %v", want, ids(runs))
	}
}

func testListRunsLimitOffset(t *testing.T, s service.Store) {
	var created []service.Run
	for range 5 {
		created = append(created, mustCreate(t, s, "x"))
	}
	// Newest first: created[4], [3], [2], [1], [0]. Offset 1, limit 2 → [3], [2].
	runs, err := s.ListRuns(ctx, service.ListRunsQuery{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if want := []string{created[3].ID, created[2].ID}; !equal(ids(runs), want) {
		t.Errorf("expected %v, got %v", want, ids(runs))
	}
}

func testListRunsIncludesCounts(t *testing.T, s service.Store) {
	run := mustCreate(t, s, "x")
	mustAdd(t, s, run.ID, result("a", service.StatusPassed), result("b", service.StatusFailed))

	runs, err := s.ListRuns(ctx, service.ListRunsQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Passed != 1 || runs[0].Failed != 1 {
		t.Errorf("expected one run with 1 passed and 1 failed, got %+v", runs)
	}
}

func testCancelledContext(t *testing.T, s service.Store) {
	run := mustCreate(t, s, "x")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	calls := map[string]func() error{
		"CreateRun": func() error { _, err := s.CreateRun(cancelled, "x", "main", "abc"); return err },
		"GetRun":    func() error { _, err := s.GetRun(cancelled, run.ID); return err },
		"AddResults": func() error {
			return s.AddResults(cancelled, run.ID, []service.TestResult{result("a", service.StatusPassed)})
		},
		"ListRuns": func() error { _, err := s.ListRuns(cancelled, service.ListRunsQuery{Limit: 1}); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: expected context.Canceled, got %v", name, err)
		}
	}
}
