package api_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	resultsv1 "github.com/adamsjoe/results-service/gen/results/v1"
	"github.com/adamsjoe/results-service/internal/memstore"
)

// FuzzREST_RequestBodies sends arbitrary bytes as the body of both POST
// endpoints. Bad input must be refused with a 4xx; the server must never
// answer 5xx, which would mean a client could make it fail. The seeds run on
// every `go test`; run `go test -fuzz=FuzzREST_RequestBodies ./test/api` to
// search further.
func FuzzREST_RequestBodies(f *testing.F) {
	f.Add([]byte(`{"suite":"s","branch":"main","commit_sha":"abc"}`))
	f.Add([]byte(`{"results":[{"name":"a","status":"STATUS_PASSED","duration_ms":3}]}`))
	f.Add([]byte(`{"results":[{"name":"a","status":"STATUS_PASSED","duration_ms":99999999999999999999}]}`))
	f.Add([]byte(`{"results":[{"name":"a","status":7,"duration_ms":-1}]}`))
	f.Add([]byte(`{"results":null}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"suite":"\u0000"}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))

	s := startServers(f, memstore.New())
	run, err := s.grpc.CreateRun(context.Background(), &resultsv1.CreateRunRequest{Suite: "s", Branch: "main", CommitSha: "abc"})
	if err != nil {
		f.Fatalf("CreateRun: %v", err)
	}
	paths := []string{"/v1/runs", "/v1/runs/" + run.GetRun().GetId() + "/results"}

	f.Fuzz(func(t *testing.T, body []byte) {
		for _, path := range paths {
			resp, err := http.Post(s.restURL+path, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("POST %s: %v", path, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 500 {
				t.Fatalf("POST %s with body %q: got %d", path, body, resp.StatusCode)
			}
		}
	})
}
