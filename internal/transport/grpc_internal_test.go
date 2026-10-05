package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/adamsjoe/results-service/internal/service"
)

func TestToStatus(t *testing.T) {
	s := &resultsServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	cases := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string // checked only when set
	}{
		{"invalid argument", fmt.Errorf("%w: suite is required", service.ErrInvalidArgument), codes.InvalidArgument, "invalid argument: suite is required"},
		{"not found", fmt.Errorf("run %q: %w", "x", service.ErrNotFound), codes.NotFound, ""},
		{"unavailable hides details", fmt.Errorf("get run: %w: %w", service.ErrUnavailable, errors.New("dial tcp 10.0.0.5:5432: connection refused")), codes.Unavailable, "service unavailable, try again later"},
		{"cancelled", context.Canceled, codes.Canceled, ""},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), codes.DeadlineExceeded, ""},
		{"unexpected error hides details", errors.New("dial tcp 10.0.0.5:5432: password authentication failed"), codes.Internal, "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := status.FromError(s.toStatus(tc.err))
			if st.Code() != tc.wantCode {
				t.Errorf("code: got %v, want %v", st.Code(), tc.wantCode)
			}
			if tc.wantMsg != "" && st.Message() != tc.wantMsg {
				t.Errorf("message: got %q, want %q", st.Message(), tc.wantMsg)
			}
		})
	}
}
