package transport

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
)

// NewGateway returns an HTTP handler serving the REST API. Each request is
// translated to a gRPC call and forwarded over conn, so REST and gRPC share
// one code path, including validation, error mapping and logging.
//
// Responses use the proto field names (snake_case, matching the API
// definition) and always include zero values, so a run with no failures
// shows "failed": 0 rather than omitting the field.
func NewGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
		}),
		runtime.WithRoutingErrorHandler(routingError),
	)
	if err := resultsv1.RegisterResultsServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}

// routingError corrects one gateway default: a known path called with the
// wrong method is reported as 501 Not Implemented. HTTP says 405.
func routingError(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, httpStatus int) {
	if httpStatus == http.StatusMethodNotAllowed {
		runtime.HTTPError(ctx, mux, m, w, r, &runtime.HTTPStatusError{
			HTTPStatus: http.StatusMethodNotAllowed,
			Err:        status.Error(codes.Unimplemented, http.StatusText(http.StatusMethodNotAllowed)),
		})
		return
	}
	runtime.DefaultRoutingErrorHandler(ctx, mux, m, w, r, httpStatus)
}
