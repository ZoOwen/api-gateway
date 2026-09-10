// Parameterized k6 script shared by every benchmark scenario in
// bench/run.sh — which scenario this is comes entirely from env vars, not
// from separate copies of this file, so all scenarios measure the exact
// same request shape.
//
// Env vars:
//   TARGET_URL  full URL to hit (gateway's /proxy/... or the upstream directly)
//   API_KEY     if set, sent as "Authorization: Bearer <API_KEY>"
//   VUS         virtual users (default 100)
//   DURATION    test duration (default 30s)
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const TARGET_URL = __ENV.TARGET_URL || 'http://localhost:8080/proxy/';
const API_KEY = __ENV.API_KEY || '';

// Separate counters for the two "expected" outcomes, so the report can
// tell "everything passed" apart from "everything got rate-limited"
// instead of lumping both into one pass/fail check.
const allowedCounter = new Counter('scenario_allowed');
const deniedCounter = new Counter('scenario_denied');
const unexpectedCounter = new Counter('scenario_unexpected');

export const options = {
  vus: Number(__ENV.VUS || 100),
  duration: __ENV.DURATION || '30s',
  // Default trend stats omit p(50)/p(99); add them so the summary export
  // has exactly what RESULTS.md needs without extra post-processing.
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    // Never fail the run itself on rate-limit rejections — 429 is an
    // expected, correct response in the "denied" scenario, not an error.
    http_req_failed: ['rate<1.0'],
  },
};

export default function () {
  const params = {};
  if (API_KEY) {
    params.headers = { Authorization: `Bearer ${API_KEY}` };
  }
  const res = http.get(TARGET_URL, params);
  check(res, {
    'status is 200 or 429': (r) => r.status === 200 || r.status === 429,
  });

  if (res.status === 200) {
    allowedCounter.add(1);
  } else if (res.status === 429) {
    deniedCounter.add(1);
  } else {
    unexpectedCounter.add(1);
  }
}
