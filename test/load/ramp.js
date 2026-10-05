// Ramp: increase the pipeline rate on one protocol until the service starts
// failing, to find where throughput tops out. The run stops itself once more
// than 5% of checks fail, so the last rate reached is the answer.
//
//   make load SCRIPT=ramp K6_ARGS="-e PROTOCOL=rest"
//   make load SCRIPT=ramp K6_ARGS="-e PROTOCOL=grpc"

import { grpcPipeline, restPipeline } from './lib.js';

const PROTOCOL = __ENV.PROTOCOL || 'rest';
const PEAK = Number(__ENV.PEAK || 400); // pipelines per second at the top of the ramp
const BATCH = Number(__ENV.BATCH || 50);

if (PROTOCOL !== 'rest' && PROTOCOL !== 'grpc') {
  throw new Error(`PROTOCOL must be rest or grpc, got ${PROTOCOL}`);
}

export const options = {
  scenarios: {
    ramp: {
      executor: 'ramping-arrival-rate',
      startRate: 10,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 500,
      stages: [
        { target: PEAK / 4, duration: '1m' },
        { target: PEAK / 2, duration: '1m' },
        { target: PEAK, duration: '1m' },
        { target: PEAK, duration: '1m' },
      ],
    },
  },
  thresholds: {
    checks: [{ threshold: 'rate>0.95', abortOnFail: true, delayAbortEval: '10s' }],
  },
};

export default function () {
  if (PROTOCOL === 'grpc') {
    grpcPipeline(BATCH);
  } else {
    restPipeline(BATCH);
  }
}
