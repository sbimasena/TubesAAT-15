import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

const errors = new Rate('uncontrolled_errors');
const served = new Counter('responses_200');
export const options = {
  vus: Number(__ENV.VUS || 10000), duration: __ENV.DURATION || '120s',
  thresholds: { uncontrolled_errors: ['rate<0.01'], responses_200: ['count>0'] },
};
export default function () {
  const response = http.get(`${__ENV.TARGET_URL}/internal/v1/hazards?source=BMKG&limit=100`, {
    timeout: '10s', headers: { 'X-Correlation-ID': `m2-p2-${__VU}-${__ITER}` },
  });
  let body;
  try { body = response.json(); } catch (_) { body = null; }
  const valid = response.status === 200 && Array.isArray(body?.data) && body.count === body.data.length;
  // An unlabelled 503 is an outage, not a controlled rejection.
  const controlled = [429, 503].includes(response.status) && body?.error?.code && response.headers['Retry-After'];
  errors.add(!valid && !controlled);
  served.add(valid ? 1 : 0);
  check(response, { 'canonical response or labelled controlled rejection': () => valid || !!controlled });
}
