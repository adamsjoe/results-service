package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/memstore"
)

// outcome is how a call ended: a gRPC status code or an HTTP status code,
// depending on the protocol.
type outcome struct {
	grpc codes.Code
	http int
}

// api is one protocol's view of the service. The shared behaviour tests run
// against both implementations, so gRPC and REST are held to the same rules.
type api interface {
	name() string
	createRun(t *testing.T, req *resultsv1.CreateRunRequest) (*resultsv1.Run, outcome)
	recordResults(t *testing.T, req *resultsv1.RecordResultsRequest) (*resultsv1.RecordResultsResponse, outcome)
	getRun(t *testing.T, id string) (*resultsv1.Run, outcome)
	listRuns(t *testing.T, req *resultsv1.ListRunsRequest) (*resultsv1.ListRunsResponse, outcome)
	// isOK reports whether o is success for this protocol.
	isOK(o outcome) bool
	// matches reports whether o is the expected failure for this protocol.
	matches(o outcome, wantGRPC codes.Code, wantHTTP int) bool
}

// forEachAPI runs fn against a fresh server pair, once per protocol.
func forEachAPI(t *testing.T, fn func(t *testing.T, a api)) {
	t.Helper()
	for _, protocol := range []string{"grpc", "rest"} {
		t.Run(protocol, func(t *testing.T) {
			s := startServers(t, memstore.New())
			var a api = grpcAPI{s.grpc}
			if protocol == "rest" {
				a = restAPI{base: s.restURL}
			}
			fn(t, a)
		})
	}
}

// ---- gRPC ---------------------------------------------------------------

type grpcAPI struct {
	c resultsv1.ResultsServiceClient
}

func (grpcAPI) name() string                                { return "grpc" }
func (grpcAPI) isOK(o outcome) bool                         { return o.grpc == codes.OK }
func (grpcAPI) matches(o outcome, g codes.Code, _ int) bool { return o.grpc == g }

func (a grpcAPI) createRun(_ *testing.T, req *resultsv1.CreateRunRequest) (*resultsv1.Run, outcome) {
	resp, err := a.c.CreateRun(context.Background(), req)
	return resp.GetRun(), outcome{grpc: status.Code(err)}
}

func (a grpcAPI) recordResults(_ *testing.T, req *resultsv1.RecordResultsRequest) (*resultsv1.RecordResultsResponse, outcome) {
	resp, err := a.c.RecordResults(context.Background(), req)
	return resp, outcome{grpc: status.Code(err)}
}

func (a grpcAPI) getRun(_ *testing.T, id string) (*resultsv1.Run, outcome) {
	resp, err := a.c.GetRun(context.Background(), &resultsv1.GetRunRequest{RunId: id})
	return resp.GetRun(), outcome{grpc: status.Code(err)}
}

func (a grpcAPI) listRuns(_ *testing.T, req *resultsv1.ListRunsRequest) (*resultsv1.ListRunsResponse, outcome) {
	resp, err := a.c.ListRuns(context.Background(), req)
	return resp, outcome{grpc: status.Code(err)}
}

// ---- REST ---------------------------------------------------------------

type restAPI struct{ base string }

func (restAPI) name() string                                { return "rest" }
func (restAPI) isOK(o outcome) bool                         { return o.http == http.StatusOK }
func (restAPI) matches(o outcome, _ codes.Code, h int) bool { return o.http == h }

// request body JSON uses the proto field names, as documented for the API.
var marshal = protojson.MarshalOptions{UseProtoNames: true}

// do sends a request and, on 200, decodes the JSON response into out. Unknown
// response fields fail the decode, so the tests catch unexpected changes.
func (a restAPI) do(t *testing.T, method, path string, body proto.Message, out proto.Message) outcome {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, err := marshal.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := protojson.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s %s response %s: %v", method, path, raw, err)
		}
	}
	return outcome{http: resp.StatusCode}
}

func (a restAPI) createRun(t *testing.T, req *resultsv1.CreateRunRequest) (*resultsv1.Run, outcome) {
	var resp resultsv1.CreateRunResponse
	o := a.do(t, http.MethodPost, "/v1/runs", req, &resp)
	return resp.GetRun(), o
}

func (a restAPI) recordResults(t *testing.T, req *resultsv1.RecordResultsRequest) (*resultsv1.RecordResultsResponse, outcome) {
	var resp resultsv1.RecordResultsResponse
	body := &resultsv1.RecordResultsRequest{Results: req.GetResults()} // run_id travels in the path
	o := a.do(t, http.MethodPost, "/v1/runs/"+url.PathEscape(req.GetRunId())+"/results", body, &resp)
	return &resp, o
}

func (a restAPI) getRun(t *testing.T, id string) (*resultsv1.Run, outcome) {
	var resp resultsv1.GetRunResponse
	o := a.do(t, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, &resp)
	return resp.GetRun(), o
}

func (a restAPI) listRuns(t *testing.T, req *resultsv1.ListRunsRequest) (*resultsv1.ListRunsResponse, outcome) {
	q := url.Values{}
	if req.GetSuite() != "" {
		q.Set("suite", req.GetSuite())
	}
	if req.GetPageSize() != 0 {
		q.Set("page_size", strconv.Itoa(int(req.GetPageSize())))
	}
	if req.GetPageToken() != "" {
		q.Set("page_token", req.GetPageToken())
	}
	var resp resultsv1.ListRunsResponse
	o := a.do(t, http.MethodGet, "/v1/runs?"+q.Encode(), nil, &resp)
	return &resp, o
}
