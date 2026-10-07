// SPDX-License-Identifier: MPL-2.0
import { it, expect, vi } from 'vitest';
import { readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { commitFile, createRepository, fixtureGit } from './fixture';

it('isolates mutating fixtures from inherited Git redirection and configuration', async () => {
  const sentinel = await createRepository();
  let fixture: string | undefined;
  try {
    const head = await commitFile(sentinel, 'sentinel.txt', 'untouched\n');
    const index = await readFile(join(sentinel, '.git/index'));
    const config = await readFile(join(sentinel, '.git/config'));
    vi.stubEnv('GIT_DIR', join(sentinel, '.git'));
    vi.stubEnv('GIT_WORK_TREE', sentinel);
    vi.stubEnv('GIT_INDEX_FILE', join(sentinel, '.git/index'));
    vi.stubEnv('GIT_CONFIG_COUNT', '1');
    vi.stubEnv('GIT_CONFIG_KEY_0', 'user.name');
    vi.stubEnv('GIT_CONFIG_VALUE_0', 'Inherited attacker');
    fixture = await createRepository();
    await commitFile(fixture, 'fixture.txt', 'isolated\n');
    expect(await fixtureGit(fixture, 'config', 'user.name')).toBe('Lúcia 山田');
    expect(await fixtureGit(sentinel, 'rev-parse', 'HEAD')).toBe(head);
    expect(await readFile(join(sentinel, '.git/index'))).toEqual(index);
    expect(await readFile(join(sentinel, '.git/config'))).toEqual(config);
    expect(await readFile(join(sentinel, 'sentinel.txt'), 'utf8')).toBe(
      'untouched\n',
    );
    await expect(readFile(join(sentinel, 'fixture.txt'))).rejects.toMatchObject(
      { code: 'ENOENT' },
    );
  } finally {
    vi.unstubAllEnvs();
    await rm(sentinel, { recursive: true, force: true });
    if (fixture) await rm(fixture, { recursive: true, force: true });
  }
});
