// SPDX-License-Identifier: MPL-2.0
import { spawn } from 'node:child_process';
import { stat } from 'node:fs/promises';
import { GitError } from './errors';
import { filterOverrides } from './configuration';

export interface GitRunOptions {
  signal?: AbortSignal;
  input?: string;
  maxBytes?: number;
  timeoutMs?: number;
  truncate?: boolean;
}
export interface GitOutput {
  stdout: string;
  exitCode: number;
  truncated: boolean;
}

// Suppress optional index writes and commands configured to execute external helpers.
const globalArgs = [
  '--no-pager',
  '--no-optional-locks',
  '--literal-pathspecs',
  '-c',
  'core.fsmonitor=false',
  '-c',
  'color.ui=false',
  '-c',
  'core.quotePath=true',
  '-c',
  'log.showSignature=false',
  '-c',
  'i18n.logOutputEncoding=UTF-8',
];

async function spawnGit(
  cwd: string,
  args: readonly string[],
  options: GitRunOptions = {},
): Promise<GitOutput> {
  try {
    if (!(await stat(cwd)).isDirectory()) throw new Error('Not a directory');
  } catch {
    throw new GitError(
      'notRepository',
      'The repository folder is missing or inaccessible. Reopen an available Git folder.',
    );
  }
  if (options.signal?.aborted)
    return Promise.reject(cancellationError(options.signal));
  const env = Object.fromEntries(
    Object.entries(process.env).filter(
      ([key]) => !key.toUpperCase().startsWith('GIT_'),
    ),
  );
  return new Promise((resolve, reject) => {
    const child = spawn('git', [...globalArgs, ...args], {
      cwd,
      shell: false,
      windowsHide: true,
      env: {
        ...env,
        GIT_TERMINAL_PROMPT: '0',
        GIT_PAGER: 'cat',
        LC_ALL: 'C',
        GIT_NO_LAZY_FETCH: '1',
        // Deny every transport as defense in depth for older Git versions.
        GIT_ALLOW_PROTOCOL: '',
        GIT_PROTOCOL_FROM_USER: '0',
      },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    const buffers: Buffer[] = [];
    const maxBytes = options.maxBytes ?? 8 * 1024 * 1024;
    let bytes = 0;
    let failure: GitError | undefined;
    let truncated = false;
    let escalation: ReturnType<typeof setTimeout> | undefined;
    const terminate = () => {
      child.kill();
      escalation ??= setTimeout(() => child.kill('SIGKILL'), 250);
    };
    const stop = (error: GitError) => {
      failure ??= error;
      terminate();
    };
    const abort = () => stop(cancellationError(options.signal));
    const timer = setTimeout(
      () =>
        stop(
          new GitError(
            'timeout',
            'Git inspection timed out. Try a smaller history page or check the repository.',
          ),
        ),
      options.timeoutMs ?? 30000,
    );
    options.signal?.addEventListener('abort', abort, { once: true });
    const cleanup = () => {
      clearTimeout(timer);
      if (escalation) clearTimeout(escalation);
      options.signal?.removeEventListener('abort', abort);
    };
    child.stdout.on('data', (chunk: Buffer) => {
      const remaining = maxBytes - bytes;
      if (remaining > 0) buffers.push(chunk.subarray(0, remaining));
      bytes += chunk.length;
      if (bytes > maxBytes) {
        if (options.truncate) {
          truncated = true;
          terminate();
        } else
          stop(
            new GitError(
              'tooLarge',
              'Git returned more data than the safety limit. Reduce the history page size.',
            ),
          );
      }
    });
    // Drain stderr without retaining repository-controlled diagnostics or credentials.
    child.stderr.resume();
    child.stdin.on('error', () => {
      /* Early Git exits may close stdin. The close event reports the result. */
    });
    child.on('error', (error: NodeJS.ErrnoException) => {
      cleanup();
      reject(
        new GitError(
          error.code === 'ENOENT' ? 'missingGit' : 'failed',
          error.code === 'ENOENT'
            ? 'Git was not found. Install Git and restart VS Code so it is available on PATH.'
            : 'Could not start Git. Check that the repository folder is accessible.',
        ),
      );
    });
    child.on('close', (code) => {
      cleanup();
      if (failure) reject(failure);
      else
        resolve({
          stdout: decodeOutput(Buffer.concat(buffers), truncated),
          exitCode: code ?? (truncated ? 0 : -1),
          truncated,
        });
    });
    child.stdin.end(options.input);
  });
}

// Coalesce concurrent configuration reads, but re-read between operations so
// changes in local configuration cannot leave a stale helper allowlist.
function cancellationError(signal?: AbortSignal): GitError {
  return signal?.reason instanceof GitError
    ? signal.reason
    : new GitError('cancelled', 'Git inspection was cancelled.');
}

// Never render a partial UTF-8 character at the preview byte limit.
function decodeOutput(bytes: Buffer, truncated: boolean): string {
  if (!truncated) return bytes.toString('utf8');
  let end = bytes.length;
  while (end > Math.max(0, bytes.length - 4)) {
    try {
      return new TextDecoder('utf-8', { fatal: true }).decode(
        bytes.subarray(0, end),
      );
    } catch {
      end--;
    }
  }
  return bytes.toString('utf8');
}

interface ConfigurationRead {
  promise: Promise<string[]>;
  controller: AbortController;
  subscribers: number;
  settled: boolean;
}
const configurations = new Map<string, ConfigurationRead>();
async function disabledFilters(
  cwd: string,
  signal?: AbortSignal,
): Promise<string[]> {
  if (signal?.aborted) throw cancellationError(signal);
  let entry = configurations.get(cwd);
  if (!entry) {
    const controller = new AbortController();
    const created: ConfigurationRead = {
      controller,
      subscribers: 0,
      settled: false,
      promise: Promise.resolve([]),
    };
    created.promise = (async () => {
      const output = await spawnGit(
        cwd,
        [
          'config',
          '--null',
          '--name-only',
          '--get-regexp',
          '^filter\\..*\\.(clean|smudge|process|required)$',
        ],
        { maxBytes: 1024 * 1024, timeoutMs: 5000, signal: controller.signal },
      );
      if (output.exitCode !== 0 && output.exitCode !== 1)
        throw new GitError(
          'failed',
          'Git configuration could not be read safely. Check repository permissions and configuration.',
        );
      return filterOverrides(output.stdout);
    })().finally(() => {
      created.settled = true;
      if (configurations.get(cwd) === created) configurations.delete(cwd);
    });
    entry = created;
    configurations.set(cwd, entry);
  }
  const shared = entry;
  shared.subscribers++;
  let abort: (() => void) | undefined;
  try {
    return await new Promise<string[]>((resolve, reject) => {
      abort = () => reject(cancellationError(signal));
      signal?.addEventListener('abort', abort, { once: true });
      shared.promise.then(resolve, reject);
      if (signal?.aborted) abort();
    });
  } finally {
    if (abort) signal?.removeEventListener('abort', abort);
    shared.subscribers--;
    if (!shared.subscribers && !shared.settled) {
      configurations.delete(cwd);
      shared.controller.abort();
    }
  }
}

export async function hasContentFilters(
  cwd: string,
  options: GitRunOptions = {},
): Promise<boolean> {
  return (await disabledFilters(cwd, options.signal)).length > 0;
}

export async function runGit(
  cwd: string,
  args: readonly string[],
  options: GitRunOptions = {},
): Promise<GitOutput> {
  if (options.signal?.aborted)
    throw new GitError('cancelled', 'Git inspection was cancelled.');
  const deadline = new AbortController();
  const timeout = setTimeout(
    () =>
      deadline.abort(
        new GitError(
          'timeout',
          'Git inspection timed out. Try a smaller history page or check the repository.',
        ),
      ),
    options.timeoutMs ?? 30000,
  );
  const signal = options.signal
    ? AbortSignal.any([options.signal, deadline.signal])
    : deadline.signal;
  try {
    const overrides = await disabledFilters(cwd, signal);
    return await spawnGit(cwd, [...overrides, ...args], { ...options, signal });
  } finally {
    clearTimeout(timeout);
  }
}

export async function gitText(
  cwd: string,
  args: readonly string[],
  options: GitRunOptions = {},
): Promise<string> {
  const result = await runGit(cwd, args, options);
  if (result.exitCode !== 0)
    throw new GitError(
      'failed',
      'Git could not read this repository. Check permissions and repository integrity, then refresh.',
    );
  return result.stdout;
}
