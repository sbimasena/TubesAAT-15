import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const elapsed = new Trend('bmkg_elapsed_ms', true);
const servedElapsed = new Trend('bmkg_served_elapsed_ms', true);
const errors = new Rate('uncontrolled_errors');
const throttled = new Rate('throttled_queries');
const queries = new Counter('query_requests');
const served = new Counter('responses_200');
const limited = new Counter('responses_429');
const refreshes = new Counter('refresh_successes');
const refreshErrors = new Rate('refresh_failures');
let session;
http.setResponseCallback(http.expectedStatuses(200, 429));

export const options = {
  scenarios: { bmkg: { executor: 'constant-vus', vus: 50, duration: '60s', gracefulStop: '5s' } },
  setupTimeout: '120s',
  noConnectionReuse: false,
  summaryTrendStats: ['min', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    bmkg_elapsed_ms: ['p(95)<300'],
    bmkg_served_elapsed_ms: ['p(95)<300'],
    uncontrolled_errors: ['rate<0.01'],
    refresh_failures: ['rate==0'],
    refresh_successes: ['count>=50'],
    responses_200: ['count>0'],
  },
};

function tokenPair(response) {
  if (response.status !== 200) return null;
  try {
    const pair = response.json();
    if (!pair.access_token || !pair.refresh_token || pair.expires_in !== 60) return null;
    return { ...pair, refreshAt: Date.now() + (pair.expires_in - 6) * 1000 };
  } catch (_) {
    return null;
  }
}

export function setup() {
  const pairs = [];
  for (let vu = 0; vu < 50; vu++) {
    const response = http.post(`${__ENV.AUTH_URL}/login`, JSON.stringify({
      client_id: __ENV.FIELD_TEAM_CLIENT_ID, password: __ENV.FIELD_TEAM_CLIENT_PASSWORD,
    }), { headers: { 'Content-Type': 'application/json' }, tags: { operation: 'login' }, timeout: '15s' });
    const pair = tokenPair(response);
    if (!pair) fail(`Field Team session ${vu + 1} could not be prepared with TTL 60s`);
    pairs.push(pair);
  }
  if (new Set(pairs.map(p => p.refresh_token)).size !== 50) fail('Sessions must have independent refresh tokens');
  console.log('P2_SESSIONS_READY');
  return pairs;
}

export default function (pairs) {
  if (!session) session = pairs[__VU - 1];
  if (Date.now() >= session.refreshAt) {
    const response = http.post(`${__ENV.AUTH_URL}/refresh`, JSON.stringify({ refresh_token: session.refresh_token }), {
      headers: { 'Content-Type': 'application/json', 'X-Correlation-ID': `p2-refresh-${__VU}-${__ITER}` },
      tags: { operation: 'refresh' }, timeout: '15s',
    });
    const next = tokenPair(response);
    refreshErrors.add(!next);
    if (!next) fail(`VU ${__VU} refresh failed`);
    session = next;
    refreshes.add(1);
  }
  const correlation = `p2-${__ENV.RUN_ID}-${__VU}-${__ITER}`;
  const started = Date.now();
  const response = http.get(`${__ENV.BASE_URL}/hazards?source=BMKG&hazard_type=SEISMIC&limit=100`, {
    headers: { Authorization: `Bearer ${session.access_token}`, 'X-Correlation-ID': correlation },
    tags: { operation: 'query' }, timeout: '15s',
  });
  const duration = Date.now() - started;
  let body;
  try { body = response.json(); } catch (_) { body = null; }
  const propagated = response.headers['X-Correlation-Id'] === correlation;
  const canonical = response.status === 200 && body && Array.isArray(body.data) && body.sources
    && body.count === body.data.length && body.count > 0
    && body.data.every(r => r.source === 'BMKG' && r.hazard_type === 'SEISMIC' && Object.keys(r).length === 11)
    && Object.keys(body.sources).length === 2;
  const controlledLimit = response.status === 429 && body && body.error
    && body.error.code === 'concurrency_limit' && body.correlation_id === correlation
    && response.headers['Retry-After'] === '1';
  const valid = propagated && (canonical || controlledLimit);
  queries.add(1);
  elapsed.add(duration);
  errors.add(!valid);
  throttled.add(response.status === 429);
  if (canonical) { served.add(1); servedElapsed.add(duration); }
  limited.add(response.status === 429 ? 1 : 0);
  check(response, { 'valid canonical read or controlled 429 with correlation': () => valid });
}

export function handleSummary(data) {
  // k6 includes setup return values, which contain the initial session tokens.
  delete data.setup_data;
  return {
    [__ENV.SUMMARY_PATH || 'client-p2-summary.json']: JSON.stringify(data, null, 2) + '\n',
    stdout: JSON.stringify(Object.fromEntries(['query_requests', 'responses_200', 'responses_429',
      'bmkg_elapsed_ms', 'bmkg_served_elapsed_ms', 'uncontrolled_errors', 'throttled_queries',
      'refresh_successes', 'refresh_failures'].map(name => [name, data.metrics[name]?.values || null])), null, 2) + '\n',
  };
}
