// P5.5: when the Client API is over capacity, Field Team reads are still served while
// Media reads are rejected in a controlled way (429/503 with an error code and Retry-After).
import http from 'k6/http';
import { Rate, Trend } from 'k6/metrics';

const fieldOK = new Rate('field_ok');
const fieldLatency = new Trend('field_latency', true);
const mediaOK = new Rate('media_ok');
const mediaControlled = new Rate('media_controlled_rejection');
const uncontrolled = new Rate('uncontrolled_errors');

const DURATION = __ENV.DURATION || '60s';
export const options = {
  scenarios: {
    media: { executor: 'constant-vus', vus: Number(__ENV.MEDIA_VUS || 400), duration: DURATION, env: { ROLE: 'media' } },
    field: { executor: 'constant-vus', vus: Number(__ENV.FIELD_VUS || 20), duration: DURATION, env: { ROLE: 'field-team' } },
  },
  thresholds: { field_ok: ['rate>0.99'], uncontrolled_errors: ['rate<0.01'] },
};

const passwords = { 'media': __ENV.MEDIA_PASSWORD, 'field-team': __ENV.FIELD_TEAM_PASSWORD };
let token = '';
let tokenAt = 0;

function login(role) {
  // Access tokens live 60 s; renew at 45 s so no request carries an expired one.
  if (token && Date.now() - tokenAt < 45000) return token;
  const res = http.post(`${__ENV.AUTH_URL}/login`, JSON.stringify({ client_id: role, password: passwords[role] }),
    { headers: { 'Content-Type': 'application/json' }, tags: { name: 'login' } });
  if (res.status === 200) { token = res.json('access_token'); tokenAt = Date.now(); }
  return token;
}

export default function () {
  const role = __ENV.ROLE;
  const res = http.get(`${__ENV.API_URL}/hazards?limit=20`, {
    headers: { Authorization: `Bearer ${login(role)}`, 'X-Correlation-ID': `p5-priority-${role}-${__VU}-${__ITER}` },
    timeout: '10s', tags: { name: 'hazards', role },
  });
  if (res.status === 401) token = ''; // session lost (Auth restart): log in again next iteration
  const ok = res.status === 200;
  let code = '';
  try { code = res.json('error.code') || ''; } catch (_) { code = ''; }
  const controlled = [429, 503].includes(res.status) && code !== '' && !!res.headers['Retry-After'];
  if (role === 'field-team') {
    fieldOK.add(ok);
    fieldLatency.add(res.timings.duration);
  } else {
    mediaOK.add(ok);
    mediaControlled.add(controlled);
  }
  uncontrolled.add(!ok && !controlled);
}
