import http from 'k6/http';
import { check } from 'k6';

// Explicit opt-in prevents accidental traffic to a deployed ALB.
if (__ENV.ALLOW_AWS_LOAD_TEST !== 'true') throw new Error('Set ALLOW_AWS_LOAD_TEST=true only after explicit approval');
const base = (__ENV.BASE_URL || '').replace(/\/$/, '');
if (!/^http:\/\/[a-z0-9-]+\.[a-z0-9-]+\.elb\.amazonaws\.com$/i.test(base)) throw new Error('BASE_URL must be this deployment’s HTTP ALB DNS URL');
const code = __ENV.CODE || '';
if (!/^[A-Za-z0-9_-]{1,64}$/.test(code)) throw new Error('CODE must be a previously created short code');
const rate = Number(__ENV.RATE || 5);
const seconds = Number(__ENV.SECONDS || 30);
if (!Number.isInteger(rate) || rate < 1 || rate > 25 || !Number.isInteger(seconds) || seconds < 10 || seconds > 120) throw new Error('RATE must be 1–25 and SECONDS 10–120');
export const options = {
  scenarios: { point: { executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration: `${seconds}s`, preAllocatedVUs: 20, maxVUs: 50 } },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};
export default function () {
  const response = http.get(`${base}/links/${encodeURIComponent(code)}`, { redirects: 0, timeout: '10s' });
  check(response, { 'status is 200': r => r.status === 200 });
}
export function handleSummary(data) {
  const values = name => data.metrics[name] ? data.metrics[name].values : null;
  return { stdout: JSON.stringify({ offered_rate_per_second: rate, duration_seconds: seconds, elapsed_ms: data.state.testRunDurationMs, requests: values('http_reqs'), latency_ms: values('http_req_duration'), errors: values('http_req_failed'), checks: values('checks'), dropped_iterations: values('dropped_iterations') }, null, 2) + '\n' };
}
