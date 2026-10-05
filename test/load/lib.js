// Shared building blocks for the load scripts.
//
// One iteration is one simulated CI pipeline: create a run, post a batch of
// results, read the run back, and list recent runs. The same pipeline runs
// over gRPC and over REST so the two protocols can be compared directly.

import grpc from 'k6/net/grpc';
import http from 'k6/http';
import { check } from 'k6';

export const GRPC_TARGET = __ENV.GRPC_TARGET || 'localhost:9090';
export const HTTP_TARGET = __ENV.HTTP_TARGET || 'http://localhost:8080';

const STATUSES = ['STATUS_PASSED', 'STATUS_PASSED', 'STATUS_PASSED', 'STATUS_FAILED', 'STATUS_SKIPPED'];

export function makeBatch(size) {
  const results = [];
  for (let i = 0; i < size; i++) {
    const status = STATUSES[i % STATUSES.length];
    results.push({
      name: `test ${i}`,
      status: status,
      duration_ms: 10 + (i % 200),
      error_message: status === 'STATUS_FAILED' ? 'assertion failed' : '',
    });
  }
  return results;
}

function newRun() {
  return { suite: `load-${__VU % 5}`, branch: 'main', commit_sha: `${__VU}-${__ITER}` };
}

// ---- gRPC -----------------------------------------------------------------

// Each VU keeps one connection open, as a real client would. The API is
// discovered through server reflection, so no proto files are needed here.
const client = new grpc.Client();
let connected = false;

function call(method, payload) {
  if (!connected) {
    client.connect(GRPC_TARGET, { plaintext: true, reflect: true });
    connected = true;
  }
  return client.invoke(`results.v1.ResultsService/${method}`, payload, { tags: { name: method } });
}

export function grpcPipeline(batchSize) {
  const created = call('CreateRun', newRun());
  if (!check(created, { 'grpc CreateRun ok': (r) => r && r.status === grpc.StatusOK })) {
    return;
  }
  const runId = created.message.run.id;

  const recorded = call('RecordResults', { run_id: runId, results: makeBatch(batchSize) });
  check(recorded, {
    'grpc RecordResults ok': (r) => r && r.status === grpc.StatusOK,
    'grpc RecordResults all accepted': (r) => r && r.message && Number(r.message.accepted) === batchSize,
  });

  const got = call('GetRun', { run_id: runId });
  check(got, { 'grpc GetRun ok': (r) => r && r.status === grpc.StatusOK });

  const listed = call('ListRuns', { page_size: 20 });
  check(listed, { 'grpc ListRuns ok': (r) => r && r.status === grpc.StatusOK });
}

// ---- REST -----------------------------------------------------------------

const JSON_HEADERS = { 'Content-Type': 'application/json' };

function post(path, body, name) {
  return http.post(`${HTTP_TARGET}${path}`, JSON.stringify(body), { headers: JSON_HEADERS, tags: { name } });
}

export function restPipeline(batchSize) {
  const created = post('/v1/runs', newRun(), 'CreateRun');
  if (!check(created, { 'rest CreateRun 200': (r) => r.status === 200 })) {
    return;
  }
  const runId = created.json('run.id');

  const recorded = post(`/v1/runs/${runId}/results`, { results: makeBatch(batchSize) }, 'RecordResults');
  check(recorded, {
    'rest RecordResults 200': (r) => r.status === 200,
    'rest RecordResults all accepted': (r) => r.status === 200 && r.json('accepted') === batchSize,
  });

  const got = http.get(`${HTTP_TARGET}/v1/runs/${runId}`, { tags: { name: 'GetRun' } });
  check(got, { 'rest GetRun 200': (r) => r.status === 200 });

  const listed = http.get(`${HTTP_TARGET}/v1/runs?page_size=20`, { tags: { name: 'ListRuns' } });
  check(listed, { 'rest ListRuns 200': (r) => r.status === 200 });
}
