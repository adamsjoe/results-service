// Soak: a moderate, mixed load on both protocols at once for 30 minutes, to
// catch slow leaks (memory, connections) that short runs miss. Local only.
//
//   make load SCRIPT=soak
//   make load SCRIPT=soak K6_ARGS="-e DURATION=10m"

import { grpcPipeline, restPipeline } from './lib.js';

const DURATION = __ENV.DURATION || '30m';

const steady = {
  executor: 'constant-arrival-rate',
  rate: 10,
  timeUnit: '1s',
  duration: DURATION,
  preAllocatedVUs: 10,
  maxVUs: 50,
};

export const options = {
  scenarios: {
    rest: { ...steady, exec: 'rest' },
    grpc: { ...steady, exec: 'grpcRun' },
  },
  thresholds: {
    checks: ['rate>0.99'],
    'http_req_duration{name:RecordResults}': ['p(95)<500'],
    'grpc_req_duration{name:RecordResults}': ['p(95)<500'],
  },
};

export function rest() {
  restPipeline(50);
}

export function grpcRun() {
  grpcPipeline(50);
}
