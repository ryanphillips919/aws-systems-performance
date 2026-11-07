import http from 'k6/http';
import { check, sleep } from 'k6';
export const options = {
  scenarios: { users: { executor: 'constant-vus', vus: Number(__ENV.VUS || 10), duration: __ENV.DURATION || '30s' } },
};
export default function () {
  const response = http.get(`${__ENV.BASE_URL || 'http://localhost:8000'}/links/seed000001`, { tags: { name: 'get_seed_link' } });
  check(response, { 'status is 200': (r) => r.status === 200 });
  sleep(Number(__ENV.THINK_TIME || 0.1));
}
