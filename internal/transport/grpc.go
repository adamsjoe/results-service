// Package transport exposes the service over gRPC. It translates between the
// generated protobuf types and the service's domain types, and maps service
// errors to gRPC status codes. It holds no business rules of its own.
package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/service"
)

// NewServer returns a gRPC server with the results service, server reflection
// (so grpcurl and k6 can discover the API) and request logging.
func NewServer(svc *service.Service, logger *slog.Logger) *grpc.Server {
	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(logUnary(logger)),
		grpc.ChainStreamInterceptor(logStream(logger)),
	)
	resultsv1.RegisterResultsServiceServer(s, &resultsServer{svc: svc, logger: logger})
	reflection.Register(s)
	return s
}

type resultsServer struct {
	resultsv1.UnimplementedResultsServiceServer
	svc    *service.Service
	logger *slog.Logger
}

func (s *resultsServer) CreateRun(ctx context.Context, req *resultsv1.CreateRunRequest) (*resultsv1.CreateRunResponse, error) {
	run, err := s.svc.CreateRun(ctx, req.GetSuite(), req.GetBranch(), req.GetCommitSha())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &resultsv1.CreateRunResponse{Run: toProtoRun(run)}, nil
}

func (s *resultsServer) RecordResults(ctx context.Context, req *resultsv1.RecordResultsRequest) (*resultsv1.RecordResultsResponse, error) {
	results := make([]service.TestResult, len(req.GetResults()))
	for i, r := range req.GetResults() {
		results[i] = fromProtoResult(r)
	}
	out, err := s.svc.RecordResults(ctx, req.GetRunId(), results)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &resultsv1.RecordResultsResponse{Accepted: int32(out.Accepted), Rejected: int32(out.Rejected)}, nil
}

// StreamResults collects the whole stream, then stores it as one batch, so a
// stream that is cancelled or fails part way stores nothing.
func (s *resultsServer) StreamResults(stream resultsv1.ResultsService_StreamResultsServer) error {
	var runID string
	var results []service.TestResult

	for first := true; ; first = false {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err // client cancelled or the connection failed
		}

		if first {
			runID = msg.GetRunId()
		} else if msg.GetRunId() != runID {
			return status.Error(codes.InvalidArgument, "every message in a stream must have the same run_id")
		}
		if len(results) == service.MaxBatchSize {
			return status.Errorf(codes.InvalidArgument, "stream exceeds the limit of %d results", service.MaxBatchSize)
		}
		results = append(results, fromProtoResult(msg.GetResult()))
	}

	out, err := s.svc.RecordResults(stream.Context(), runID, results)
	if err != nil {
		return s.toStatus(err)
	}
	return stream.SendAndClose(&resultsv1.StreamResultsResponse{Accepted: int32(out.Accepted), Rejected: int32(out.Rejected)})
}

func (s *resultsServer) GetRun(ctx context.Context, req *resultsv1.GetRunRequest) (*resultsv1.GetRunResponse, error) {
	run, err := s.svc.GetRun(ctx, req.GetRunId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &resultsv1.GetRunResponse{Run: toProtoRun(run)}, nil
}

func (s *resultsServer) ListRuns(ctx context.Context, req *resultsv1.ListRunsRequest) (*resultsv1.ListRunsResponse, error) {
	page, err := s.svc.ListRuns(ctx, req.GetSuite(), int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, s.toStatus(err)
	}
	runs := make([]*resultsv1.Run, len(page.Runs))
	for i, r := range page.Runs {
		runs[i] = toProtoRun(r)
	}
	return &resultsv1.ListRunsResponse{Runs: runs, NextPageToken: page.NextPageToken}, nil
}

// toStatus maps a service error to a gRPC status. Unexpected errors are logged
// in full but returned as a bare "internal error", so database or
// infrastructure details never reach the client.
func (s *resultsServer) toStatus(err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, service.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, service.ErrUnavailable):
		s.logger.Warn("storage unavailable", "error", err)
		return status.Error(codes.Unavailable, "service unavailable, try again later")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	default:
		s.logger.Error("internal error", "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func fromProtoResult(r *resultsv1.TestResult) service.TestResult {
	return service.TestResult{
		Name:         r.GetName(),
		Status:       fromProtoStatus(r.GetStatus()),
		DurationMS:   r.GetDurationMs(),
		ErrorMessage: r.GetErrorMessage(),
	}
}

func fromProtoStatus(s resultsv1.Status) service.Status {
	switch s {
	case resultsv1.Status_STATUS_PASSED:
		return service.StatusPassed
	case resultsv1.Status_STATUS_FAILED:
		return service.StatusFailed
	case resultsv1.Status_STATUS_SKIPPED:
		return service.StatusSkipped
	default:
		return service.StatusUnspecified // rejected by the service
	}
}

func toProtoRun(r service.Run) *resultsv1.Run {
	return &resultsv1.Run{
		Id:        r.ID,
		Suite:     r.Suite,
		Branch:    r.Branch,
		CommitSha: r.CommitSHA,
		StartedAt: timestamppb.New(r.StartedAt),
		Passed:    int32(r.Passed),
		Failed:    int32(r.Failed),
		Skipped:   int32(r.Skipped),
	}
}

// Successful calls log at debug level, so load tests do not flood the logs;
// failed calls log at info level.
func logCall(logger *slog.Logger, method string, start time.Time, err error) {
	code := status.Code(err)
	level := slog.LevelDebug
	if code != codes.OK {
		level = slog.LevelInfo
	}
	logger.Log(context.Background(), level, "grpc call",
		"method", method, "code", code.String(), "duration_ms", time.Since(start).Milliseconds())
}

func logUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logCall(logger, info.FullMethod, start, err)
		return resp, err
	}
}

func logStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logCall(logger, info.FullMethod, start, err)
		return err
	}
}
