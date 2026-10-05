package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/adamsjoe/results-service/internal/memstore"
	"github.com/adamsjoe/results-service/internal/service"
)

// FuzzListRuns_PageInput feeds arbitrary page tokens and sizes to ListRuns.
// Whatever arrives, the answer must be a page or ErrInvalidArgument, never a
// panic or any other error. The seeds run on every `go test`; run
// `go test -fuzz=FuzzListRuns_PageInput ./internal/service` to search further.
func FuzzListRuns_PageInput(f *testing.F) {
	f.Add("", 0)
	f.Add("b2Zmc2V0OjA", 10)                           // offset:0
	f.Add("b2Zmc2V0Oi0x", 10)                          // offset:-1
	f.Add("b2Zmc2V0Ojk5OTk5OTk5OTk5OTk5OTk5OTk5OQ", 5) // overflowing offset
	f.Add("not base64!!", -1)
	f.Add("b2Zmc2V0OjE", 1<<31-1)

	svc := service.New(memstore.New())
	for range 3 {
		if _, err := svc.CreateRun(context.Background(), "s", "main", "abc"); err != nil {
			f.Fatalf("CreateRun: %v", err)
		}
	}

	f.Fuzz(func(t *testing.T, token string, pageSize int) {
		page, err := svc.ListRuns(context.Background(), "", pageSize, token)
		if err != nil {
			if !errors.Is(err, service.ErrInvalidArgument) {
				t.Fatalf("token %q, size %d: unexpected error type: %v", token, pageSize, err)
			}
			return
		}
		if len(page.Runs) > service.MaxPageSize {
			t.Fatalf("page of %d runs exceeds the maximum", len(page.Runs))
		}
	})
}
