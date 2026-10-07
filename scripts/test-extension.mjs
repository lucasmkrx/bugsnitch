// SPDX-License-Identifier: MPL-2.0
import { mkdir, mkdtemp, rm, writeFile, readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import { build } from 'esbuild';
import {
  runTests,
  downloadAndUnzipVSCode,
  resolveCliArgsFromVSCodeExecutablePath,
} from '@vscode/test-electron';
import { createDemoFixture } from './demo-fixture.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const fixture = await createDemoFixture();
const installed = process.argv.includes('--installed');
const cache = join(root, 'apps/vscode/.cache', 'extension-tests');
await mkdir(cache, { recursive: true });
const profile = await mkdtemp(join(cache, 'profile-'));
const observer = join(profile, 'observer');
const extensionDirectory = join(profile, 'extensions');
const developmentPath = installed ? observer : join(root, 'apps/vscode');
let tests = join(cache, 'extension-host.cjs');
const version = process.env.BUGSNITCH_TEST_VSCODE_VERSION ?? '1.105.0';
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
let packagePath;
let packageDigest;
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
  const executable =
    process.env.BUGSNITCH_TEST_VSCODE_EXECUTABLE ??
    (await downloadAndUnzipVSCode({
      version,
      cachePath: join(root, '.vscode-test'),
    }));
  let testedPath = join(root, 'apps/vscode');
  if (installed) {
    const manifest = JSON.parse(
      await readFile(join(testedPath, 'package.json'), 'utf8'),
    );
    packagePath = join(testedPath, `bugsnitch-${manifest.version}.vsix`);
    packageDigest = digest(await readFile(packagePath));
    const receipt = JSON.parse(
      await readFile(join(root, '.cache/package-validation.json'), 'utf8'),
    );
    if (receipt.sha256 !== packageDigest)
      throw new Error('Package differs from validated artifact');
    await mkdir(observer, { recursive: true });
    await writeFile(
      join(observer, 'package.json'),
      JSON.stringify({
        name: 'bugsnitch-test-observer',
        publisher: 'local-test',
        version: '0.0.0',
        engines: { vscode: '^1.105.0' },
        main: './observer.cjs',
      }),
    );
    await writeFile(
      join(observer, 'observer.cjs'),
      'exports.activate = () => {};',
    );
    const [cli, ...cliArgs] =
      resolveCliArgsFromVSCodeExecutablePath(executable);
    await promisify(execFile)(
      cli,
      [
        ...cliArgs,
        '--user-data-dir',
        profile,
        '--extensions-dir',
        extensionDirectory,
        '--install-extension',
        packagePath,
        '--force',
      ],
      { timeout: 120_000 },
    );
    testedPath = join(
      extensionDirectory,
      `${manifest.publisher}.${manifest.name}-${manifest.version}`,
    );
    await readFile(join(testedPath, 'package.json'));
    // VS Code scopes its API by module location. Add only the observer runner
    // beside the installed files; packaged manifest/resources remain untouched.
    tests = join(testedPath, '.test-observer', 'extension-host.cjs');
    await mkdir(join(testedPath, '.test-observer'), { recursive: true });
  }
  await build({
    entryPoints: [join(root, 'apps/vscode/test/extension-host.ts')],
    outfile: tests,
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node22',
    external: ['vscode'],
  });
  await runTests({
    version,
    vscodeExecutablePath: executable,
    extensionDevelopmentPath: developmentPath,
    extensionTestsPath: tests,
    reuseMachineInstall: false,
    cachePath: join(root, '.vscode-test'),
    launchArgs: [
      fixture.root,
      ...(!installed ? ['--disable-extensions'] : []),
      '--disable-workspace-trust',
      '--skip-welcome',
      '--skip-release-notes',
      '--user-data-dir',
      profile,
      '--extensions-dir',
      extensionDirectory,
    ],
    extensionTestsEnv: {
      BUGSNITCH_TEST_FIXTURE: JSON.stringify(fixture),
      BUGSNITCH_TEST_EXTENSION_PATH: testedPath,
    },
  });
  if (installed) {
    if (digest(await readFile(packagePath)) !== packageDigest)
      throw new Error('Package changed during installed smoke');
    console.log(`PASS: installed VSIX SHA256 ${packageDigest}`);
  }
} finally {
  for (const path of [fixture.root, profile])
    await rm(path, {
      recursive: true,
      force: true,
      maxRetries: 5,
      retryDelay: 200,
    });
}
