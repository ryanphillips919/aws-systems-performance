import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

const base = __ENV.BASE_URL || 'http://api:8000';
const mode = __ENV.MODE || 'point';
const rate = Number(__ENV.RATE || 5);
const duration = __ENV.DURATION || '10s';
const code = __ENV.CODE || 'seed000006';
const user = __ENV.USER_ID || 'user001';
if (!['point', 'user', 'popular', 'mixed'].includes(mode)) throw new Error('MODE must be point, user, popular or mixed');
export const options = {
  scenarios: { workload: { executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration, preAllocatedVUs: Number(__ENV.VUS || 20), maxVUs: Number(__ENV.MAX_VUS || 100) } },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};
export default function () {
  const selected = mode === 'mixed' ? ['point', 'user', 'popular'][exec.scenario.iterationInTest % 3] : mode;
  const paths = { point: `/links/${encodeURIComponent(code)}`, user: `/users/${encodeURIComponent(user)}/links?limit=50`, popular: '/analytics/popular?limit=10' };
  const response = http.get(`${base}${paths[selected]}`, { redirects: 0, tags: { name: selected } });
  check(response, { 'status is 200': r => r.status === 200 });
}
export function handleSummary(data) {
  const values = name => data.metrics[name] ? data.metrics[name].values : null;
  const result = { mode, offered_rate_per_second: rate, configured_duration: duration, code, user, elapsed_ms: data.state.testRunDurationMs,
    requests: values('http_reqs'), achieved_requests_per_second: values('http_reqs') ? values('http_reqs').rate : 0, latency_ms: values('http_req_duration'), failed: values('http_req_failed'), checks: values('checks'), dropped_iterations: values('dropped_iterations') };
  return { stdout: JSON.stringify(result, null, 2) + '\n' };
}
