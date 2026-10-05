// Package service holds the business rules for ingesting test results.
//
// It knows nothing about gRPC, HTTP or SQL: transports call Service, and
// storage backends implement Store.
package service

import (
	"context"
	"errors"
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

// errNotImplemented is returned by every method until the rules are written.
var errNotImplemented = errors.New("not implemented")

// RecordOutcome reports how many results in a batch were stored or rejected.
type RecordOutcome struct {
	Accepted int
	Rejected int
}

// RunPage is one page of runs and the token for the next page, if any.
type RunPage struct {
	Runs          []Run
	NextPageToken string // empty on the last page
}

// CreateRun starts a new run.
func (s *Service) CreateRun(ctx context.Context, suite, branch, commitSHA string) (Run, error) {
	return Run{}, errNotImplemented
}

// RecordResults stores a batch of results against a run.
func (s *Service) RecordResults(ctx context.Context, runID string, results []TestResult) (RecordOutcome, error) {
	return RecordOutcome{}, errNotImplemented
}

// GetRun returns a run with its result counts.
func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	return Run{}, errNotImplemented
}

// ListRuns returns runs newest first, optionally filtered by suite.
func (s *Service) ListRuns(ctx context.Context, suite string, pageSize int, pageToken string) (RunPage, error) {
	return RunPage{}, errNotImplemented
}
