import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

const errors = new Rate('uncontrolled_errors');
const failed = new Rate('failed_responses');
const served = new Counter('responses_200');
const thresholds = { uncontrolled_errors: ['rate<0.01'], responses_200: ['count>0'] };
if (__ENV.REQUIRE_ZERO_FAILURES === '1') thresholds.failed_responses = ['rate==0'];
// TARGET_URL may list several comma-separated base URLs; VUs are spread over them.
const targets = (__ENV.TARGET_URL || '').split(',');
export const options = {
  vus: Number(__ENV.VUS || 10000), duration: __ENV.DURATION || '120s',
  thresholds,
};
export default function () {
  const response = http.get(`${targets[__VU % targets.length]}/internal/v1/hazards?source=BMKG&limit=100`, {
    timeout: '10s', headers: { 'X-Correlation-ID': `m2-p2-${__VU}-${__ITER}` },
  });
  let body;
  try { body = response.json(); } catch (_) { body = null; }
  const valid = response.status === 200 && Array.isArray(body?.data) && body.count === body.data.length;
  // An unlabelled 503 is an outage, not a controlled rejection.
  const controlled = [429, 503].includes(response.status) && body?.error?.code && response.headers['Retry-After'];
  errors.add(!valid && !controlled);
  failed.add(!valid);
  served.add(valid ? 1 : 0);
  check(response, { 'canonical response or labelled controlled rejection': () => valid || !!controlled });
}
