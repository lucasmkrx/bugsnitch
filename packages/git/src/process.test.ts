// SPDX-License-Identifier: MPL-2.0
import { expect, it, vi } from 'vitest';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
const state = vi.hoisted(() => ({
  hangConfig: false,
  children: [] as {
    command: string;
    close(): void;
    kill: ReturnType<typeof vi.fn>;
  }[],
}));
vi.mock('node:child_process', () => ({
  spawn: (_exe: string, args: string[]) => {
    const child = new EventEmitter();
    const isConfig = args.includes('config');
    const kill = vi.fn((signal?: string) => {
      if (signal === 'SIGKILL') queueMicrotask(() => child.emit('close', null));
      return true;
    });
    Object.assign(child, {
      stdout: new PassThrough(),
      stderr: new PassThrough(),
      stdin: new PassThrough(),
      kill,
    });
    state.children.push({
      command: isConfig ? 'config' : 'command',
      close: () => child.emit('close', 1),
      kill,
    });
    if (isConfig && !state.hangConfig)
      queueMicrotask(() => child.emit('close', 1));
    return child;
  },
}));
import { runGit } from './process';
it('cancels during configuration discovery and escalates child termination', async () => {
  state.hangConfig = true;
  state.children = [];
  const controller = new AbortController();
  const result = runGit(process.cwd(), ['status'], {
    signal: controller.signal,
  });
  const assertion = expect(result).rejects.toMatchObject({ code: 'cancelled' });
  await vi.waitFor(() => expect(state.children).toHaveLength(1));
  controller.abort();
  await assertion;
  await vi.waitFor(() =>
    expect(state.children[0]!.kill).toHaveBeenCalledWith('SIGKILL'),
  );
});
it('one cancelled borrower does not cancel a shared configuration read', async () => {
  state.hangConfig = true;
  state.children = [];
  const controller = new AbortController();
  const one = runGit(process.cwd(), ['status'], { signal: controller.signal });
  const cancelled = expect(one).rejects.toMatchObject({ code: 'cancelled' });
  const two = runGit(process.cwd(), ['status'], { timeoutMs: 100 });
  const timedOut = expect(two).rejects.toMatchObject({ code: 'timeout' });
  await vi.waitFor(() => expect(state.children).toHaveLength(1));
  controller.abort();
  await cancelled;
  expect(state.children[0]!.kill).not.toHaveBeenCalled();
  state.children[0]!.close();
  await timedOut;
  expect(state.children[1]!.command).toBe('command');
  expect(state.children[1]!.kill).toHaveBeenCalledWith('SIGKILL');
});
