// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import { webviewHtml } from './webview-html';

it('restricts network and executable content while escaping URI attributes', () => {
  const html = webviewHtml(
    'vscode-resource:',
    'script.js" onload="oops',
    'style.css',
    'logo.svg',
  );
  expect(html).toContain('default-src &#39;none&#39;');
  expect(html).toContain('connect-src &#39;none&#39;');
  expect(html).not.toContain('src="script.js" onload=');
  expect(html).toMatch(/<script nonce="[A-Za-z0-9+/=]+"/);
  expect(html).not.toContain("'unsafe-inline'");
  expect(webviewHtml('source:', 's', 'c', 'l')).not.toBe(
    webviewHtml('source:', 's', 'c', 'l'),
  );
});
