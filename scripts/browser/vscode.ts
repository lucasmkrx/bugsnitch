// SPDX-License-Identifier: MPL-2.0
import type { HostMessage } from '../../packages/shared/src/index';
import type * as VSCode from 'vscode';
let send: (message: HostMessage) => Promise<boolean> = async () => true;
let receive: ((value: unknown) => unknown) | undefined;
let dispose: (() => void) | undefined;
let html = '';
let base = '';
export function configure(url: string, post: typeof send) {
  base = url;
  send = post;
}
export function receiveMessage(message: unknown) {
  return receive?.(message);
}
export function getHtml() {
  return html;
}
export const Uri = {
  joinPath: (uri: { toString?: () => string }, ...paths: string[]) => ({
    toString: () =>
      `${uri.toString && uri.toString() !== '[object Object]' ? uri.toString() : base}/${paths.join('/')}`,
  }),
  from: ({
    scheme,
    path,
    query,
  }: {
    scheme: string;
    path: string;
    query: string;
  }) => ({ toString: () => `${scheme}:${path}?${query}` }),
};
export const ViewColumn = { Beside: 2 };
export const workspace = {
  getConfiguration: () => ({ get: () => 10 }),
  registerTextDocumentContentProvider: () => ({ dispose() {} }),
};
export const env = { clipboard: { writeText: async () => {} } };
export const commands = { executeCommand: async () => {} };
export const window = {
  createWebviewPanel: () =>
    ({
      webview: {
        cspSource: base,
        get html() {
          return html;
        },
        set html(value: string) {
          html = value;
        },
        asWebviewUri: (uri: { toString(): string }) => uri,
        postMessage: (message: HostMessage) => send(message),
        onDidReceiveMessage: (handler: typeof receive) => {
          receive = handler;
          return { dispose() {} };
        },
      },
      reveal() {},
      dispose() {
        dispose?.();
      },
      onDidDispose: (handler: () => void) => {
        dispose = handler;
        return { dispose() {} };
      },
    }) as unknown as VSCode.WebviewPanel,
};
