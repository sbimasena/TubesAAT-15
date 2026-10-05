import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const elapsed = new Trend('bmkg_elapsed_ms', true);
const errors = new Rate('uncontrolled_errors');
const throttled = new Counter('responses_429');

export const options = {
  scenarios: { bmkg: { executor: 'constant-vus', vus: 50, duration: '60s', gracefulStop: '5s' } },
  noConnectionReuse: false,
  summaryTrendStats: ['min', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    bmkg_elapsed_ms: ['p(95)<300'],
    uncontrolled_errors: ['rate<0.01'],
    checks: ['rate==1'],
  },
};

export default function () {
  const correlation = `p2-load-${__VU}-${__ITER}`;
  const started = Date.now();
  const response = http.get(`${__ENV.BASE_URL || 'http://127.0.0.1:8083'}/internal/v1/hazards?source=BMKG&hazard_type=SEISMIC&limit=100`, {
    headers: { 'X-Correlation-ID': correlation }, timeout: '5s',
  });
  elapsed.add(Date.now() - started); // Includes connection setup and full body download.
  // No rate limiter exists: every non-200 (including 429) is an uncontrolled error.
  errors.add(response.status !== 200);
  throttled.add(response.status === 429 ? 1 : 0);
  check(response, {
    'HTTP 200': r => r.status === 200,
    'correlation propagated': r => r.headers['X-Correlation-Id'] === correlation,
    'BMKG canonical response': r => r.status === 200 && r.json('data.0.source') === 'BMKG' && r.json('data.0.hazard_type') === 'SEISMIC',
  });
}

export function handleSummary(data) {
  return {
    [__ENV.SUMMARY_PATH || 'p2-summary.json']: JSON.stringify(data, null, 2) + '\n',
    stdout: JSON.stringify({
      requests: data.metrics.http_reqs.values,
      elapsed_ms: data.metrics.bmkg_elapsed_ms.values,
      errors: data.metrics.uncontrolled_errors.values,
      responses_429: data.metrics.responses_429.values,
      checks: data.metrics.checks.values,
    }, null, 2) + '\n',
  };
}
