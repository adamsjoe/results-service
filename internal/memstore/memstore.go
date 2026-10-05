// Package memstore is an in-memory service.Store, used as the test double for
// service logic and, later, for in-process API tests.
package memstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/adamsjoe/results-service/internal/service"
)

// Store keeps runs and results in memory. It is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	runs []*entry // in creation order
	byID map[string]*entry
}

type entry struct {
	run     service.Run
	results []service.TestResult
}

var _ service.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{byID: map[string]*entry{}}
}

// CreateRun implements service.Store.
func (s *Store) CreateRun(ctx context.Context, suite, branch, commitSHA string) (service.Run, error) {
	if err := ctx.Err(); err != nil {
		return service.Run{}, err
	}
	id, err := newID()
	if err != nil {
		return service.Run{}, err
	}

	e := &entry{run: service.Run{
		ID:        id,
		Suite:     suite,
		Branch:    branch,
		CommitSHA: commitSHA,
		StartedAt: time.Now().UTC(),
	}}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, e)
	s.byID[id] = e
	return e.run, nil
}

// GetRun implements service.Store.
func (s *Store) GetRun(ctx context.Context, id string) (service.Run, error) {
	if err := ctx.Err(); err != nil {
		return service.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.byID[id]
	if !ok {
		return service.Run{}, fmt.Errorf("run %q: %w", id, service.ErrNotFound)
	}
	return withCounts(e), nil
}

// AddResults implements service.Store.
func (s *Store) AddResults(ctx context.Context, runID string, results []service.TestResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.byID[runID]
	if !ok {
		return fmt.Errorf("run %q: %w", runID, service.ErrNotFound)
	}
	e.results = append(e.results, results...)
	return nil
}

// ListRuns implements service.Store.
func (s *Store) ListRuns(ctx context.Context, q service.ListRunsQuery) ([]service.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []service.Run
	skipped := 0
	for i := len(s.runs) - 1; i >= 0 && len(out) < q.Limit; i-- { // newest first
		e := s.runs[i]
		if q.Suite != "" && e.run.Suite != q.Suite {
			continue
		}
		if skipped < q.Offset {
			skipped++
			continue
		}
		out = append(out, withCounts(e))
	}
	return out, nil
}

// Results returns a copy of the results stored for a run, for test assertions.
func (s *Store) Results(runID string) []service.TestResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.byID[runID]
	if !ok {
		return nil
	}
	return append([]service.TestResult(nil), e.results...)
}

func withCounts(e *entry) service.Run {
	run := e.run
	for _, r := range e.results {
		switch r.Status {
		case service.StatusPassed:
			run.Passed++
		case service.StatusFailed:
			run.Failed++
		case service.StatusSkipped:
			run.Skipped++
		}
	}
	return run
}

// newID returns a random version 4 UUID.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
