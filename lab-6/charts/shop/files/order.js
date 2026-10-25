import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const v1 = new Counter('responses_v1');
const v2 = new Counter('responses_v2');

export const options = {
  scenarios: {
    orders: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 50),
      timeUnit: '1s',
      duration: __ENV.DURATION || '120s',
      preAllocatedVUs: 20,
      maxVUs: 50,
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  discardResponseBodies: true,
};

export default function () {
  const base = __ENV.TARGET || 'http://shop-api:8080';
  const response = __ENV.MODE === 'version'
    ? http.get(`${base}/version`, { responseType: 'text' })
    : http.post(`${base}/order`, JSON.stringify({ item: 'lab6', quantity: 1 }), {
      headers: { 'Content-Type': 'application/json' },
      timeout: '3s',
    });
  check(response, { success: r => r.status === (__ENV.MODE === 'version' ? 200 : 201) });
  const version = __ENV.MODE === 'version' ? response.json('version') : response.headers['X-App-Version'];
  if (version === 'v1') v1.add(1);
  if (version === 'v2') v2.add(1);
}
