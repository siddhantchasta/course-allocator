import http from 'k6/http';
import { check } from 'k6';

export const options = {
  vus: 10,        // 10 virtual users generating peak concurrency
  duration: '5s',
};

const targetUrl = __ENV.TARGET_URL || 'http://127.0.0.1:9090/register/atomic';

export default function () {
  const res = http.post(targetUrl);
  check(res, { 'is status 200': (r) => r.status === 200 });
}