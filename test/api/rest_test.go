package api_test

// REST-only behaviour: the JSON shape clients see, how bad requests are
// handled before they reach the service, and HTTP status mapping.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/adamsjoe/results-service/internal/memstore"
)

func restServer(t *testing.T) string {
	t.Helper()
	return startServers(t, memstore.New()).restURL
}

// send makes a raw HTTP request and returns the status and body.
func send(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, raw
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

func TestREST_ResponseUsesSnakeCaseAndIncludesZeroCounts(t *testing.T) {
	base := restServer(t)
	code, raw := send(t, http.MethodPost, base+"/v1/runs", `{"suite":"s","branch":"main","commit_sha":"abc"}`)
	if code != http.StatusOK {
		t.Fatalf("create: %d %s", code, raw)
	}
	run, _ := decode(t, raw)["run"].(map[string]any)

	for _, key := range []string{"id", "suite", "branch", "commit_sha", "started_at", "passed", "failed", "skipped"} {
		if _, ok := run[key]; !ok {
			t.Errorf("expected key %q in %s", key, raw)
		}
	}
	if _, ok := run["commitSha"]; ok {
		t.Errorf("expected snake_case keys only, got camelCase in %s", raw)
	}
	if run["failed"] != float64(0) {
		t.Errorf(`expected "failed": 0, got %v`, run["failed"])
	}
}

func TestREST_AcceptsEnumNamesAndCamelCaseInput(t *testing.T) {
	base := restServer(t)
	_, raw := send(t, http.MethodPost, base+"/v1/runs", `{"suite":"s","branch":"main","commitSha":"abc"}`)
	id, _ := decode(t, raw)["run"].(map[string]any)["id"].(string)
	if id == "" {
		t.Fatalf("camelCase commitSha not accepted: %s", raw)
	}

	code, raw := send(t, http.MethodPost, base+"/v1/runs/"+id+"/results",
		`{"results":[{"name":"a","status":"STATUS_PASSED","duration_ms":3}]}`)
	if code != http.StatusOK {
		t.Fatalf("record: %d %s", code, raw)
	}
	if got := decode(t, raw)["accepted"]; got != float64(1) {
		t.Errorf("expected 1 accepted, got %v", got)
	}
}

func TestREST_BadRequestsAreRejectedWith400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"suite":`},
		{"unknown field", `{"suite":"s","branch":"main","commit_sha":"abc","colour":"blue"}`},
		{"wrong type", `{"suite":123,"branch":"main","commit_sha":"abc"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := send(t, http.MethodPost, restServer(t)+"/v1/runs", tc.body)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", code, raw)
			}
		})
	}
}

func TestREST_InvalidEnumValueIsRejectedWith400(t *testing.T) {
	base := restServer(t)
	_, raw := send(t, http.MethodPost, base+"/v1/runs", `{"suite":"s","branch":"main","commit_sha":"abc"}`)
	id, _ := decode(t, raw)["run"].(map[string]any)["id"].(string)

	code, raw := send(t, http.MethodPost, base+"/v1/runs/"+id+"/results",
		`{"results":[{"name":"a","status":"STATUS_EXPLODED","duration_ms":3}]}`)
	if code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", code, raw)
	}
}

func TestREST_ErrorBodyCarriesCodeAndMessage(t *testing.T) {
	code, raw := send(t, http.MethodGet, restServer(t)+"/v1/runs/"+unknownRunID, "")
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", code, raw)
	}
	body := decode(t, raw)
	if body["code"] != float64(5) { // gRPC NotFound
		t.Errorf("expected code 5, got %v", body["code"])
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "not found") {
		t.Errorf("expected a not found message, got %q", msg)
	}
}

func TestREST_UnexpectedErrorIs500WithoutDetails(t *testing.T) {
	base := startServers(t, brokenStore{memstore.New()}).restURL
	code, raw := send(t, http.MethodGet, base+"/v1/runs/any", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", code, raw)
	}
	if msg := decode(t, raw)["message"]; msg != "internal error" {
		t.Errorf("expected only \"internal error\", got %q", msg)
	}
}

func TestREST_RoutesAndMethods(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"unknown path", http.MethodGet, "/v1/nothing", http.StatusNotFound},
		{"empty run id", http.MethodGet, "/v1/runs/", http.StatusBadRequest},
		{"wrong method", http.MethodDelete, "/v1/runs", http.StatusMethodNotAllowed},
		{"stream is gRPC only", http.MethodPost, "/v1/runs/x/stream", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := send(t, tc.method, restServer(t)+tc.path, "")
			if code != tc.want {
				t.Errorf("%s %s: expected %d, got %d: %s", tc.method, tc.path, tc.want, code, raw)
			}
		})
	}
}
