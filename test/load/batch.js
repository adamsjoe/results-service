// Batch size: the same request rate with batches of 10, 100 and 1,000
// results, on each protocol in turn. Shows what payload size costs, and where
// protobuf's compact encoding pulls ahead of JSON.
//
//   make load SCRIPT=batch

import { grpcPipeline, restPipeline } from './lib.js';

const SIZES = [10, 100, 1000];
const STEP = Number(__ENV.STEP || 30); // seconds per scenario
const GAP = 5;

const scenarios = {};
const thresholds = { checks: ['rate>0.99'] };
let start = 0;

for (const protocol of ['rest', 'grpc']) {
  for (const size of SIZES) {
    const name = `${protocol}_${size}`;
    scenarios[name] = {
      executor: 'constant-arrival-rate',
      rate: 5,
      timeUnit: '1s',
      duration: `${STEP}s`,
      preAllocatedVUs: 10,
      maxVUs: 50,
      startTime: `${start}s`,
      exec: protocol === 'grpc' ? 'grpcRun' : 'rest',
      env: { BATCH: String(size) },
    };
    // No limits; listed so the summary shows RecordResults per batch size.
    const metric = protocol === 'grpc' ? 'grpc_req_duration' : 'http_req_duration';
    thresholds[`${metric}{scenario:${name},name:RecordResults}`] = ['p(95)>=0'];
    start += STEP + GAP;
  }
}

export const options = { scenarios, thresholds };

export function rest() {
  restPipeline(Number(__ENV.BATCH));
}

export function grpcRun() {
  grpcPipeline(Number(__ENV.BATCH));
}
