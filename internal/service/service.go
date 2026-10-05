// Package service holds the business rules for ingesting test results.
//
// It knows nothing about gRPC, HTTP or SQL: transports call Service, and
// storage backends implement Store.
package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Errors returned by Service. Transports map these to status codes.
var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotFound        = errors.New("not found")
)

// Limits applied by Service.
const (
	MaxBatchSize    = 1000
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Status is the outcome of a single test.
type Status int

// Valid statuses. StatusUnspecified is the zero value and is never accepted.
const (
	StatusUnspecified Status = iota
	StatusPassed
	StatusFailed
	StatusSkipped
)

func (s Status) valid() bool {
	return s >= StatusPassed && s <= StatusSkipped
}

// Run is one execution of a test suite, with counts of its results.
type Run struct {
	ID        string
	Suite     string
	Branch    string
	CommitSHA string
	StartedAt time.Time
	Passed    int
	Failed    int
	Skipped   int
}

// TestResult is the outcome of one test within a run.
type TestResult struct {
	Name         string
	Status       Status
	DurationMS   int64
	ErrorMessage string
}

// ListRunsQuery selects a page of runs from a Store.
type ListRunsQuery struct {
	Suite  string // empty means all suites
	Limit  int
	Offset int
}

// Store persists runs and results.
type Store interface {
	// CreateRun stores a new run, assigning its ID and StartedAt.
	CreateRun(ctx context.Context, suite, branch, commitSHA string) (Run, error)
	// GetRun returns the run with its result counts, or ErrNotFound.
	GetRun(ctx context.Context, id string) (Run, error)
	// AddResults stores results for a run atomically. It returns ErrNotFound
	// if the run does not exist, even when results is empty.
	AddResults(ctx context.Context, runID string, results []TestResult) error
	// ListRuns returns runs newest first, with result counts.
	ListRuns(ctx context.Context, q ListRunsQuery) ([]Run, error)
}

// Service applies validation and paging rules on top of a Store.
type Service struct {
	store Store
}

// New returns a Service backed by store.
func New(store Store) *Service {
	return &Service{store: store}
}

// CreateRun starts a new run. Suite, branch and commit SHA are all required.
func (s *Service) CreateRun(ctx context.Context, suite, branch, commitSHA string) (Run, error) {
	required := []struct{ field, value string }{
		{"suite", suite},
		{"branch", branch},
		{"commit_sha", commitSHA},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return Run{}, fmt.Errorf("%w: %s is required", ErrInvalidArgument, r.field)
		}
	}
	return s.store.CreateRun(ctx, suite, branch, commitSHA)
}

// RecordOutcome reports how many results in a batch were stored or rejected.
type RecordOutcome struct {
	Accepted int
	Rejected int
}

// RecordResults stores a batch of results against a run. Invalid results are
// rejected individually; valid results in the same batch are still stored.
// The batch itself must hold between 1 and MaxBatchSize results.
func (s *Service) RecordResults(ctx context.Context, runID string, results []TestResult) (RecordOutcome, error) {
	if strings.TrimSpace(runID) == "" {
		return RecordOutcome{}, fmt.Errorf("%w: run_id is required", ErrInvalidArgument)
	}
	if len(results) == 0 {
		return RecordOutcome{}, fmt.Errorf("%w: at least one result is required", ErrInvalidArgument)
	}
	if len(results) > MaxBatchSize {
		return RecordOutcome{}, fmt.Errorf("%w: batch of %d exceeds the limit of %d", ErrInvalidArgument, len(results), MaxBatchSize)
	}

	valid := make([]TestResult, 0, len(results))
	for _, r := range results {
		if validResult(r) {
			valid = append(valid, r)
		}
	}

	// Called even when nothing is valid, so an unknown run is still reported.
	if err := s.store.AddResults(ctx, runID, valid); err != nil {
		return RecordOutcome{}, err
	}
	return RecordOutcome{Accepted: len(valid), Rejected: len(results) - len(valid)}, nil
}

func validResult(r TestResult) bool {
	return strings.TrimSpace(r.Name) != "" && r.Status.valid() && r.DurationMS >= 0
}

// GetRun returns a run with its result counts.
func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	if strings.TrimSpace(id) == "" {
		return Run{}, fmt.Errorf("%w: run_id is required", ErrInvalidArgument)
	}
	return s.store.GetRun(ctx, id)
}

// RunPage is one page of runs and the token for the next page, if any.
type RunPage struct {
	Runs          []Run
	NextPageToken string // empty on the last page
}

// ListRuns returns runs newest first, optionally filtered by suite.
// A page size of 0 means DefaultPageSize; sizes above MaxPageSize are capped.
func (s *Service) ListRuns(ctx context.Context, suite string, pageSize int, pageToken string) (RunPage, error) {
	switch {
	case pageSize < 0:
		return RunPage{}, fmt.Errorf("%w: page_size must not be negative", ErrInvalidArgument)
	case pageSize == 0:
		pageSize = DefaultPageSize
	case pageSize > MaxPageSize:
		pageSize = MaxPageSize
	}

	offset, err := decodePageToken(pageToken)
	if err != nil {
		return RunPage{}, err
	}

	// Ask for one extra run to learn whether another page exists.
	runs, err := s.store.ListRuns(ctx, ListRunsQuery{Suite: suite, Limit: pageSize + 1, Offset: offset})
	if err != nil {
		return RunPage{}, err
	}

	page := RunPage{Runs: runs}
	if len(runs) > pageSize {
		page.Runs = runs[:pageSize]
		page.NextPageToken = encodePageToken(offset + pageSize)
	}
	return page, nil
}

const tokenPrefix = "offset:"

func encodePageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(tokenPrefix + strconv.Itoa(offset)))
}

func decodePageToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	invalid := fmt.Errorf("%w: page_token is not valid", ErrInvalidArgument)

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, invalid
	}
	value, ok := strings.CutPrefix(string(raw), tokenPrefix)
	if !ok {
		return 0, invalid
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, invalid
	}
	return offset, nil
}
