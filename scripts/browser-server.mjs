// SPDX-License-Identifier: MPL-2.0
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { build } from 'esbuild';
await build({
  entryPoints: ['scripts/browser/host.ts'],
  outfile: '.cache/browser-host.cjs',
  bundle: true,
  platform: 'node',
  format: 'cjs',
  alias: { vscode: resolve('scripts/browser/vscode.ts') },
});
const server = createServer(async (request, response) => {
  try {
    const path = new URL(request.url, 'http://localhost').pathname;
    const allowed = {
      '/dist/webview/webview.js': [
        'apps/vscode/dist/webview/webview.js',
        'text/javascript',
      ],
      '/dist/webview/webview.css': [
        'apps/vscode/dist/webview/webview.css',
        'text/css',
      ],
      '/dist/brand/bugsnitch-logo.svg': [
        'apps/vscode/dist/brand/bugsnitch-logo.svg',
        'image/svg+xml',
      ],
    };
    if (path === '/') {
      response.setHeader('Content-Type', 'text/html; charset=utf-8');
      response.end('<!doctype html><title>Browser harness</title>');
      return;
    }
    const resource = allowed[path];
    if (!resource) {
      response.writeHead(404);
      response.end();
      return;
    }
    response.setHeader('Content-Type', `${resource[1]}; charset=utf-8`);
    response.end(await readFile(resource[0]));
  } catch {
    response.writeHead(500);
    response.end('Test resource unavailable');
  }
});
server.listen(4319, '127.0.0.1');
for (const signal of ['SIGINT', 'SIGTERM'])
  process.on(signal, () => server.close(() => process.exit(0)));
