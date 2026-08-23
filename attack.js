import http from 'k6/http';
import { check } from 'k6';

export const options = {
  vus: 10,        // Start with 10 virtual users to trigger the race condition
  duration: '5s',
};

export default function () {
  // Hit the new registration endpoint
  const res = http.post('http://127.0.0.1:9090/register/vulnerable');
  
  if (res.status !== 200) {
     console.warn(`Error: ${res.status} ${res.body}`);
  }

  check(res, { 'is status 200': (r) => r.status === 200 });
}