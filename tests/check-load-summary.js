import { handleSummary as summarize } from './load-client-p2.js';

export const options = { vus: 1, iterations: 1 };

export function setup() {
  return [{ access_token: 'test-access-secret', refresh_token: 'test-refresh-secret' }];
}

export default function () {}

export function handleSummary(data) {
  if (!data.setup_data) throw new Error('Expected k6 setup data for this regression check');
  const result = summarize(data);
  const json = result[__ENV.SUMMARY_PATH || 'client-p2-summary.json'];
  const saved = JSON.parse(json);
  if ('setup_data' in saved || json.includes('test-access-secret') || json.includes('test-refresh-secret')) {
    throw new Error('Session credentials leaked into the load summary');
  }
  return { stdout: 'PASS: k6 setup credentials are omitted from the saved summary\n' };
}
