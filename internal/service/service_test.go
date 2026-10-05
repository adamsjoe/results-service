package service_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/adamsjoe/results-service/internal/memstore"
	"github.com/adamsjoe/results-service/internal/service"
)

func newService(t *testing.T) (*service.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	return service.New(store), store
}

func mustCreateRun(t *testing.T, svc *service.Service, suite string) service.Run {
	t.Helper()
	run, err := svc.CreateRun(context.Background(), suite, "main", "abc123")
	if err != nil {
		t.Fatalf("CreateRun: unexpected error: %v", err)
	}
	return run
}

func passed(name string) service.TestResult {
	return service.TestResult{Name: name, Status: service.StatusPassed, DurationMS: 10}
}

func batch(n int) []service.TestResult {
	results := make([]service.TestResult, n)
	for i := range results {
		results[i] = passed("test")
	}
	return results
}

// ---- CreateRun ---------------------------------------------------------

func TestCreateRun_ValidInputReturnsRunWithIDAndZeroCounts(t *testing.T) {
	svc, _ := newService(t)

	run, err := svc.CreateRun(context.Background(), "checkout-e2e", "main", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.ID == "" {
		t.Error("expected an ID to be assigned")
	}
	if run.Suite != "checkout-e2e" || run.Branch != "main" || run.CommitSHA != "abc123" {
		t.Errorf("fields not echoed back: %+v", run)
	}
	if run.StartedAt.IsZero() {
		t.Error("expected StartedAt to be set")
	}
	if run.Passed != 0 || run.Failed != 0 || run.Skipped != 0 {
		t.Errorf("expected zero counts, got %+v", run)
	}
}

func TestCreateRun_MissingFieldIsInvalidArgument(t *testing.T) {
	cases := []struct {
		name, suite, branch, commit string
	}{
		{"missing suite", "", "main", "abc123"},
		{"missing branch", "checkout-e2e", "", "abc123"},
		{"missing commit", "checkout-e2e", "main", ""},
		{"whitespace-only suite", "   ", "main", "abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t)
			_, err := svc.CreateRun(context.Background(), tc.suite, tc.branch, tc.commit)
			if !errors.Is(err, service.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument, got %v", err)
			}
		})
	}
}

// ---- RecordResults -----------------------------------------------------

func TestRecordResults_AllValidAreAccepted(t *testing.T) {
	svc, _ := newService(t)
	run := mustCreateRun(t, svc, "suite")

	got, err := svc.RecordResults(context.Background(), run.ID, batch(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Accepted != 3 || got.Rejected != 0 {
		t.Errorf("expected 3 accepted, 0 rejected; got %+v", got)
	}
}

func TestRecordResults_InvalidResultsAreRejectedAndValidOnesStillLand(t *testing.T) {
	svc, store := newService(t)
	run := mustCreateRun(t, svc, "suite")

	results := []service.TestResult{
		passed("valid one"),
		{Name: "", Status: service.StatusPassed, DurationMS: 1},                 // empty name
		{Name: "unspecified", Status: service.StatusUnspecified, DurationMS: 1}, // no status
		{Name: "out of range", Status: service.Status(99), DurationMS: 1},       // unknown status
		{Name: "negative", Status: service.StatusPassed, DurationMS: -1},        // negative duration
		{Name: "valid two", Status: service.StatusFailed, DurationMS: 5, ErrorMessage: "boom"},
	}

	got, err := svc.RecordResults(context.Background(), run.ID, results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Accepted != 2 || got.Rejected != 4 {
		t.Errorf("expected 2 accepted, 4 rejected; got %+v", got)
	}

	stored := store.Results(run.ID)
	if len(stored) != 2 || stored[0].Name != "valid one" || stored[1].Name != "valid two" {
		t.Errorf("expected only the two valid results stored, got %+v", stored)
	}
}

func TestRecordResults_BatchAtLimitIsAccepted(t *testing.T) {
	svc, _ := newService(t)
	run := mustCreateRun(t, svc, "suite")

	got, err := svc.RecordResults(context.Background(), run.ID, batch(service.MaxBatchSize))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Accepted != service.MaxBatchSize {
		t.Errorf("expected %d accepted, got %d", service.MaxBatchSize, got.Accepted)
	}
}

func TestRecordResults_BadRequestIsInvalidArgument(t *testing.T) {
	cases := []struct {
		name    string
		runID   func(service.Run) string
		results []service.TestResult
	}{
		{"empty run id", func(service.Run) string { return "" }, batch(1)},
		{"empty batch", func(r service.Run) string { return r.ID }, nil},
		{"batch over limit", func(r service.Run) string { return r.ID }, batch(service.MaxBatchSize + 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newService(t)
			run := mustCreateRun(t, svc, "suite")

			_, err := svc.RecordResults(context.Background(), tc.runID(run), tc.results)
			if !errors.Is(err, service.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument, got %v", err)
			}
			if n := len(store.Results(run.ID)); n != 0 {
				t.Errorf("expected nothing stored, got %d results", n)
			}
		})
	}
}

func TestRecordResults_UnknownRunIsNotFound(t *testing.T) {
	cases := []struct {
		name    string
		results []service.TestResult
	}{
		{"with valid results", batch(1)},
		{"with only invalid results", []service.TestResult{{Name: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t)
			_, err := svc.RecordResults(context.Background(), "no-such-run", tc.results)
			if !errors.Is(err, service.ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
		})
	}
}

// ---- GetRun ------------------------------------------------------------

func TestGetRun_ReturnsCountsByStatus(t *testing.T) {
	svc, _ := newService(t)
	run := mustCreateRun(t, svc, "suite")
	results := []service.TestResult{
		passed("a"), passed("b"),
		{Name: "c", Status: service.StatusFailed, DurationMS: 1, ErrorMessage: "boom"},
		{Name: "d", Status: service.StatusSkipped, DurationMS: 0},
	}
	if _, err := svc.RecordResults(context.Background(), run.ID, results); err != nil {
		t.Fatalf("RecordResults: %v", err)
	}

	got, err := svc.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Passed != 2 || got.Failed != 1 || got.Skipped != 1 {
		t.Errorf("expected 2 passed, 1 failed, 1 skipped; got %+v", got)
	}
}

func TestGetRun_Errors(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr error
	}{
		{"empty id", "", service.ErrInvalidArgument},
		{"unknown id", "no-such-run", service.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t)
			_, err := svc.GetRun(context.Background(), tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// ---- ListRuns ----------------------------------------------------------

func TestListRuns_NewestFirst(t *testing.T) {
	svc, _ := newService(t)
	first := mustCreateRun(t, svc, "suite")
	second := mustCreateRun(t, svc, "suite")
	third := mustCreateRun(t, svc, "suite")

	got, err := svc.ListRuns(context.Background(), "", 0, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{third.ID, second.ID, first.ID}
	if len(got.Runs) != len(want) {
		t.Fatalf("expected %d runs, got %d", len(want), len(got.Runs))
	}
	for i, id := range want {
		if got.Runs[i].ID != id {
			t.Errorf("position %d: expected %s, got %s", i, id, got.Runs[i].ID)
		}
	}
}

func TestListRuns_FiltersBySuite(t *testing.T) {
	svc, _ := newService(t)
	mustCreateRun(t, svc, "checkout")
	mustCreateRun(t, svc, "search")
	mustCreateRun(t, svc, "checkout")

	got, err := svc.ListRuns(context.Background(), "checkout", 0, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(got.Runs))
	}
	for _, r := range got.Runs {
		if r.Suite != "checkout" {
			t.Errorf("unexpected suite %q", r.Suite)
		}
	}
}

func TestListRuns_PageSize(t *testing.T) {
	cases := []struct {
		name      string
		created   int
		pageSize  int
		wantRuns  int
		wantToken bool
	}{
		{"zero uses the default", service.DefaultPageSize + 5, 0, service.DefaultPageSize, true},
		{"over the maximum is capped", service.MaxPageSize + 20, 500, service.MaxPageSize, true},
		{"fewer runs than the page", 3, 10, 3, false},
		{"exactly one full page", 10, 10, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t)
			for range tc.created {
				mustCreateRun(t, svc, "suite")
			}

			got, err := svc.ListRuns(context.Background(), "", tc.pageSize, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Runs) != tc.wantRuns {
				t.Errorf("expected %d runs, got %d", tc.wantRuns, len(got.Runs))
			}
			if (got.NextPageToken != "") != tc.wantToken {
				t.Errorf("next page token present = %v, want %v", got.NextPageToken != "", tc.wantToken)
			}
		})
	}
}

func TestListRuns_PagingVisitsEveryRunOnce(t *testing.T) {
	svc, _ := newService(t)
	const total = 45
	for range total {
		mustCreateRun(t, svc, "suite")
	}

	seen := map[string]bool{}
	token := ""
	pages := 0
	for {
		got, err := svc.ListRuns(context.Background(), "", 20, token)
		if err != nil {
			t.Fatalf("page %d: unexpected error: %v", pages+1, err)
		}
		pages++
		for _, r := range got.Runs {
			if seen[r.ID] {
				t.Fatalf("run %s returned twice", r.ID)
			}
			seen[r.ID] = true
		}
		if got.NextPageToken == "" {
			break
		}
		token = got.NextPageToken
	}

	if len(seen) != total {
		t.Errorf("expected %d runs across all pages, got %d", total, len(seen))
	}
	if pages != 3 {
		t.Errorf("expected 3 pages, got %d", pages)
	}
}

func TestListRuns_BadInputIsInvalidArgument(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name     string
		pageSize int
		token    string
	}{
		{"negative page size", -1, ""},
		{"token not base64", 0, "not base64!!"},
		{"token wrong format", 0, enc("garbage")},
		{"token negative offset", 0, enc("offset:-1")},
		{"token non-numeric offset", 0, enc("offset:abc")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t)
			_, err := svc.ListRuns(context.Background(), "", tc.pageSize, tc.token)
			if !errors.Is(err, service.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument, got %v", err)
			}
		})
	}
}
