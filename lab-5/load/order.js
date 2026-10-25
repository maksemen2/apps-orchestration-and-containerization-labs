import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    orders: {
      executor: 'constant-arrival-rate', rate: 10, timeUnit: '1s',
      duration: '3m', preAllocatedVUs: 10, maxVUs: 30,
    },
  },
  thresholds: { http_req_failed: ['rate==0'], checks: ['rate==1'], dropped_iterations: ['count==0'] },
  noConnectionReuse: true,
};

export default function () {
  const response = http.post(
    __ENV.TARGET || 'http://shop-api.shop.svc.cluster.local:8080/order',
    JSON.stringify({ item: 'lab5-chaos', quantity: 1 }),
    { headers: { 'Content-Type': 'application/json' }, timeout: '5s' },
  );
  check(response, { 'order created': r => r.status === 201 });
}
