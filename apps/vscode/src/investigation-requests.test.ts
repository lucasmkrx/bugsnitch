// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import { InvestigationRequests } from './investigation-requests';
it('keeps the latest invocation even when repository discovery completes out of order', () => {
  const requests = new InvestigationRequests();
  const older = requests.begin('a');
  const newer = requests.begin('b');
  expect(newer.claim('repo')).toBe(true);
  newer.finish();
  expect(older.claim('repo')).toBe(false);
  expect(newer.current('repo')).toBe(true);
  const latest = requests.begin('a');
  expect(older.signal.aborted).toBe(true);
  expect(latest.claim('repo')).toBe(true);
  expect(newer.current('repo')).toBe(false);
  requests.dispose();
  expect(latest.signal.aborted).toBe(true);
  expect(requests.begin('c').signal.aborted).toBe(true);
});
