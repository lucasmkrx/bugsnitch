// SPDX-License-Identifier: MPL-2.0
import { afterEach, describe, expect, it, vi } from 'vitest';
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { pathToFileURL } from 'node:url';
import {
  blameLine,
  findRepository,
  inspectCommit,
  recentHistory,
  readCommit,
  relativeGitPath,
  repositorySnapshot,
} from './repository';
import { runGit } from './process';
import { commitFile, createRepository, fixtureGit } from '../test/fixture';

const roots: string[] = [];
async function repo() {
  const root = await createRepository();
  roots.push(root);
  return root;
}
afterEach(async () => {
  vi.unstubAllEnvs();
  await Promise.all(
    roots.splice(0).map((root) => rm(root, { recursive: true, force: true })),
  );
});

describe('local Git inspection', () => {
  it('detects repositories and keeps empty repository history useful', async () => {
    const root = await repo();
    expect(await findRepository(root)).toBe(root);
    expect(await recentHistory(root)).toEqual({ commits: [], hasMore: false });
    expect(await repositorySnapshot(root)).toMatchObject({
      head: null,
      branch: 'main',
      status: { staged: 0, modified: 0, untracked: 0, conflicted: 0 },
    });
    const outside = await mkdtemp(join(tmpdir(), 'bugsnitch outside '));
    roots.push(outside);
    expect(await findRepository(outside)).toBeNull();
    await expect(blameLine(root, 'missing', 1, 'text')).rejects.toMatchObject({
      code: 'emptyRepository',
    });
  });
  it('parses Unicode authors, multiline messages, control separators, refs and bounded pages', async () => {
    const root = await repo();
    const first = await commitFile(
      root,
      'file with spaces.txt',
      'first\n',
      'A subject with | and \u001e\u001f\n\nA body with <script>alert(1)</script>\nUnicode 🐞',
    );
    await fixtureGit(root, 'tag', 'v0.1.0');
    await commitFile(
      root,
      'file with spaces.txt',
      'first\nsecond\n',
      'Second change',
    );
    const page = await recentHistory(root, { limit: 1 });
    expect(page.hasMore).toBe(true);
    expect(page.commits).toHaveLength(1);
    expect(page.commits[0]?.author).toBe('Lúcia 山田');
    expect(page.commits[0]?.refs).toContain('HEAD -> main');
    const older = await recentHistory(root, { limit: 1, skip: 1 });
    expect(older.hasMore).toBe(false);
    expect(older.commits[0]).toMatchObject({
      hash: first,
      refs: ['tag: v0.1.0'],
    });
    expect(older.commits[0]?.message).toContain(
      '\u001e\u001f\n\nA body with <script>',
    );
    expect((await inspectCommit(root, first)).files).toEqual([
      { status: 'A', path: 'file with spaces.txt' },
    ]);
    expect((await inspectCommit(root, first)).diff).toContain('+first');
  });
  it('follows a renamed Unicode path and attributes exactly one current line', async () => {
    const root = await repo();
    const hash = await commitFile(
      root,
      'old space 山.txt',
      'one\ntwo\nthree\n',
    );
    await fixtureGit(root, 'mv', 'old space 山.txt', 'new space 山.txt');
    await fixtureGit(root, 'commit', '-m', 'Rename');
    const result = await blameLine(
      root,
      'new space 山.txt',
      2,
      'one\ntwo\nthree\n',
    );
    expect(result).toMatchObject({
      hash,
      originalLine: 2,
      currentLine: 2,
      path: 'new space 山.txt',
      originalPath: 'old space 山.txt',
      uncommitted: false,
    });
    const local = await blameLine(
      root,
      'new space 山.txt',
      2,
      'one\nchanged\nthree\n',
    );
    expect(local.uncommitted).toBe(true);
  });
  it('handles literal pathspec characters and leading dashes safely', async () => {
    const root = await repo();
    const path = '-odd [file] *.txt';
    const hash = await commitFile(root, path, 'literal\n');
    expect((await blameLine(root, path, 1, 'literal\n')).hash).toBe(hash);
    expect(relativeGitPath(root, join(root, path))).toBe(path);
    expect(() => relativeGitPath(root, join(root, '..', 'outside'))).toThrow(
      'outside',
    );
    await expect(inspectCommit(root, '--help')).rejects.toMatchObject({
      code: 'invalidInput',
    });
    await expect(blameLine(root, '../outside', 1, '')).rejects.toMatchObject({
      code: 'invalidInput',
    });
  });
  it('summarizes staged renames, modified files and untracked files', async () => {
    const root = await repo();
    await commitFile(root, 'one.txt', 'one\n');
    await fixtureGit(root, 'mv', 'one.txt', 'renamed.txt');
    await writeFile(join(root, 'renamed.txt'), 'edited\n');
    await writeFile(join(root, 'untracked.txt'), 'untracked\n');
    expect((await repositorySnapshot(root)).status).toEqual({
      staged: 1,
      modified: 1,
      untracked: 1,
      conflicted: 0,
    });
    await expect(
      blameLine(root, 'untracked.txt', 1, 'untracked\n'),
    ).rejects.toMatchObject({ code: 'untracked' });
    await expect(
      blameLine(root, 'renamed.txt', 1, '\0binary'),
    ).rejects.toMatchObject({ code: 'binary' });
    await expect(
      blameLine(root, 'renamed.txt', 999, 'edited\n'),
    ).rejects.toMatchObject({ code: 'failed' });
  });
  it('compares merge commits against their first parent and handles detached HEAD', async () => {
    const root = await repo();
    await commitFile(root, 'base.txt', 'base\n');
    await fixtureGit(root, 'checkout', '-b', 'feature');
    await commitFile(root, 'feature.txt', 'feature\n');
    await fixtureGit(root, 'checkout', 'main');
    await commitFile(root, 'main.txt', 'main\n');
    await fixtureGit(
      root,
      'merge',
      '--no-ff',
      'feature',
      '-m',
      'Merge feature',
    );
    const hash = await fixtureGit(root, 'rev-parse', 'HEAD');
    const detail = await inspectCommit(root, hash);
    expect(detail.commit.parents).toHaveLength(2);
    expect(detail.files).toEqual([{ status: 'A', path: 'feature.txt' }]);
    expect(detail.diff).toContain('+feature');
    await fixtureGit(root, 'checkout', '--detach', hash);
    expect((await repositorySnapshot(root)).branch).toContain('Detached HEAD');
  });
  it('keeps read-only Git calls from invoking configured diff and fsmonitor helpers', async () => {
    const root = await repo();
    const hash = await commitFile(root, 'text.txt', 'one\n');
    const marker = join(root, 'helper-ran');
    const helper = join(root, 'helper.sh');
    await writeFile(helper, '#!/bin/sh\ntouch helper-ran\n');
    await chmod(helper, 0o755);
    await fixtureGit(root, 'config', 'diff.external', helper);
    await fixtureGit(root, 'config', 'core.fsmonitor', helper);
    await fixtureGit(root, 'config', 'log.showSignature', 'true');
    await fixtureGit(root, 'config', 'gpg.program', helper);
    const tree = await fixtureGit(root, 'rev-parse', 'HEAD^{tree}');
    const signed = join(root, 'signed-commit.txt');
    await writeFile(
      signed,
      `tree ${tree}\nauthor Fixture <fixture@example.invalid> 1767348000 +0000\ncommitter Fixture <fixture@example.invalid> 1767348000 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n invalid\n -----END PGP SIGNATURE-----\n\nSigned fixture\n`,
    );
    const signedHash = await fixtureGit(
      root,
      'hash-object',
      '-t',
      'commit',
      '-w',
      signed,
    );
    const before = await readFile(join(root, '.git', 'index'));
    await repositorySnapshot(root);
    await inspectCommit(root, hash);
    expect((await readCommit(root, signedHash)).subject).toBe('Signed fixture');
    await expect(readFile(marker)).rejects.toMatchObject({ code: 'ENOENT' });
    expect(await readFile(join(root, '.git', 'index'))).toEqual(before);
  });
  it('never executes content filters and explains filtered-file provenance limits', async () => {
    const root = await repo();
    const hash = await commitFile(root, 'app.ts', 'original\n');
    await commitFile(root, '.gitattributes', '*.ts filter=guard\n');
    const helper = join(root, 'filter.sh');
    await writeFile(helper, '#!/bin/sh\ntouch filter-ran\ncat\n');
    await fixtureGit(root, 'config', 'filter.guard.clean', `sh "${helper}"`);
    await fixtureGit(root, 'config', 'filter.guard.smudge', `sh "${helper}"`);
    await fixtureGit(root, 'config', 'filter.guard.process', `sh "${helper}"`);
    await fixtureGit(root, 'config', 'filter.guard.required', 'true');
    await writeFile(join(root, 'app.ts'), 'local change\n');
    expect((await repositorySnapshot(root)).filtersDisabled).toBe(true);
    await inspectCommit(root, hash);
    await expect(
      blameLine(root, 'app.ts', 1, 'local change\n'),
    ).rejects.toThrow('content filter');
    await expect(readFile(join(root, 'filter-ran'))).rejects.toMatchObject({
      code: 'ENOENT',
    });
  });
  it('does not hydrate partial clones or execute a promisor transport helper', async () => {
    const source = await repo();
    const hash = await commitFile(
      source,
      'remote.txt',
      'content available only from origin\n',
    );
    await fixtureGit(source, 'config', 'uploadpack.allowFilter', 'true');
    const clone = await mkdtemp(join(tmpdir(), 'bugsnitch partial '));
    roots.push(clone);
    await fixtureGit(
      source,
      'clone',
      '--filter=blob:none',
      '--no-checkout',
      pathToFileURL(source).toString(),
      clone,
    );
    const marker = join(clone, 'transport-ran');
    const helper = join(clone, 'uploadpack.sh');
    await writeFile(helper, `#!/bin/sh\ntouch "${marker}"\n`);
    await chmod(helper, 0o755);
    await fixtureGit(clone, 'config', 'remote.origin.uploadpack', helper);
    await fixtureGit(clone, 'config', 'protocol.file.allow', 'always');
    await expect(inspectCommit(clone, hash)).rejects.toMatchObject({
      code: 'failed',
    });
    await expect(readFile(marker)).rejects.toMatchObject({ code: 'ENOENT' });
  });
  it('caps large patches, rejects large metadata and supports cancellation and missing Git', async () => {
    const root = await repo();
    const hash = await commitFile(root, 'large.txt', `${'x'.repeat(300000)}\n`);
    const detail = await inspectCommit(root, hash);
    expect(detail.diffTruncated).toBe(true);
    expect(Buffer.byteLength(detail.diff)).toBeLessThanOrEqual(256 * 1024);
    await expect(
      runGit(root, ['log', '-1'], { maxBytes: 4 }),
    ).rejects.toMatchObject({ code: 'tooLarge' });
    const controller = new AbortController();
    controller.abort();
    await expect(
      runGit(root, ['status'], { signal: controller.signal }),
    ).rejects.toMatchObject({ code: 'cancelled' });
    vi.stubEnv('PATH', join(root, 'no-git-here'));
    await expect(runGit(root, ['status'])).rejects.toMatchObject({
      code: 'missingGit',
    });
  });
});
