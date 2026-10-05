// Baseline: a steady, modest load on each protocol in turn, to record normal
// latency. REST runs first, then gRPC, so they never compete for the server.
//
//   make load                                   # 2 minutes per protocol
//   make load K6_ARGS="-e DURATION=30s"         # the CI smoke run
//
// Thresholds sit at roughly 5x the p95 of the first recorded baseline (see
// the README), leaving headroom for slower shared CI runners while still
// catching a real regression.

import { grpcPipeline, restPipeline } from './lib.js';

const DURATION = __ENV.DURATION || '2m';
const RATE = Number(__ENV.RATE || 10); // pipelines per second
const BATCH = Number(__ENV.BATCH || 50);

function seconds(d) {
  const m = /^(\d+)(s|m)$/.exec(d);
  if (!m) throw new Error(`DURATION must look like 30s or 2m, got ${d}`);
  return Number(m[1]) * (m[2] === 'm' ? 60 : 1);
}

const steady = {
  executor: 'constant-arrival-rate',
  rate: RATE,
  timeUnit: '1s',
  duration: DURATION,
  preAllocatedVUs: 10,
  maxVUs: 50,
};

export const options = {
  scenarios: {
    rest: { ...steady, exec: 'rest' },
    grpc: { ...steady, exec: 'grpcRun', startTime: `${seconds(DURATION) + 5}s` },
  },
  thresholds: {
    checks: ['rate>0.99'],
    'http_req_duration{scenario:rest,name:CreateRun}': ['p(95)<20'],
    'http_req_duration{scenario:rest,name:RecordResults}': ['p(95)<30'],
    'http_req_duration{scenario:rest,name:GetRun}': ['p(95)<10'],
    'http_req_duration{scenario:rest,name:ListRuns}': ['p(95)<50'],
    'grpc_req_duration{scenario:grpc,name:CreateRun}': ['p(95)<20'],
    'grpc_req_duration{scenario:grpc,name:RecordResults}': ['p(95)<30'],
    'grpc_req_duration{scenario:grpc,name:GetRun}': ['p(95)<10'],
    'grpc_req_duration{scenario:grpc,name:ListRuns}': ['p(95)<50'],
  },
};

export function rest() {
  restPipeline(BATCH);
}

export function grpcRun() {
  grpcPipeline(BATCH);
}
