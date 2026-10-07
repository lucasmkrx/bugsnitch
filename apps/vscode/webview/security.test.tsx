// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { CommitInspection, CommitRow } from '@bugsnitch/ui';
import type { Commit } from '@bugsnitch/shared';

it('renders repository-controlled text without creating executable markup', () => {
  const payload = '<script>alert("repository")</script>';
  const commit: Commit = {
    hash: 'a'.repeat(40),
    shortHash: 'aaaaaaa',
    parents: [],
    author: payload,
    authorDate: '2026-01-02T10:00:00Z',
    subject: payload,
    message: payload,
    refs: [payload],
  };
  const row = renderToStaticMarkup(
    <CommitRow
      commit={commit}
      selected={false}
      disabled={false}
      onInspect={() => {}}
    />,
  );
  const detail = renderToStaticMarkup(
    <CommitInspection
      detail={{
        commit,
        files: [{ path: payload, status: 'A' }],
        diff: payload,
        diffTruncated: false,
      }}
    />,
  );
  for (const markup of [row, detail]) {
    expect(markup).not.toContain('<script>');
    expect(markup).toContain('&lt;script&gt;');
  }
});
