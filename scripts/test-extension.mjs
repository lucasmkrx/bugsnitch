// SPDX-License-Identifier: MPL-2.0
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { build } from 'esbuild';
import { runTests } from '@vscode/test-electron';
import { createDemoFixture } from './demo-fixture.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const fixture = await createDemoFixture();
// Keep the runner inside the extension so VS Code gives it the same scoped API.
const cache = join(root, 'apps/vscode/.cache', 'extension-tests');
const tests = join(cache, 'extension-host.cjs');
await mkdir(cache, { recursive: true });
const profile = await mkdtemp(join(cache, 'profile-'));
try {
  await mkdir(join(profile, 'User'), { recursive: true });
  await writeFile(
    join(profile, 'User', 'settings.json'),
    JSON.stringify({
      'security.workspace.trust.enabled': false,
      'git.enabled': false,
      'extensions.autoUpdate': false,
      'extensions.autoCheckUpdates': false,
      'update.mode': 'none',
      'telemetry.telemetryLevel': 'off',
      'bugsnitch.historyPageSize': 10,
    }),
  );
  await build({
    entryPoints: [join(root, 'apps/vscode/test/extension-host.ts')],
    outfile: tests,
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node22',
    external: ['vscode'],
  });
  const options = {
    version: process.env.BUGSNITCH_TEST_VSCODE_VERSION ?? '1.105.0',
    extensionDevelopmentPath: join(root, 'apps/vscode'),
    extensionTestsPath: tests,
    reuseMachineInstall: false,
    cachePath: join(root, '.vscode-test'),
    launchArgs: [
      fixture.root,
      '--disable-extensions',
      '--disable-workspace-trust',
      '--skip-welcome',
      '--skip-release-notes',
      '--user-data-dir',
      profile,
      '--extensions-dir',
      join(fixture.root, '.test-extensions'),
    ],
    extensionTestsEnv: { BUGSNITCH_TEST_FIXTURE: JSON.stringify(fixture) },
  };
  if (process.env.BUGSNITCH_TEST_VSCODE_EXECUTABLE)
    options.vscodeExecutablePath = process.env.BUGSNITCH_TEST_VSCODE_EXECUTABLE;
  await runTests(options);
} finally {
  await rm(fixture.root, {
    recursive: true,
    force: true,
    maxRetries: 5,
    retryDelay: 200,
  });
  await rm(profile, {
    recursive: true,
    force: true,
    maxRetries: 5,
    retryDelay: 200,
  });
}
