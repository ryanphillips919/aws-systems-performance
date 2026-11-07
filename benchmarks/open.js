import http from 'k6/http';
import { check } from 'k6';
export const options = {
  scenarios: { arrivals: { executor: 'constant-arrival-rate', rate: Number(__ENV.RATE || 50), timeUnit: '1s', duration: __ENV.DURATION || '30s', preAllocatedVUs: Number(__ENV.VUS || 50), maxVUs: Number(__ENV.MAX_VUS || 200) } },
};
export default function () {
  const response = http.get(`${__ENV.BASE_URL || 'http://localhost:8000'}/links/seed000001`, { tags: { name: 'get_seed_link' } });
  check(response, { 'status is 200': (r) => r.status === 200 });
}
