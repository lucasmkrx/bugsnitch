// SPDX-License-Identifier: MPL-2.0
import { randomBytes } from 'node:crypto';

function escapeAttribute(value: string): string {
  return value.replace(
    /[&"<>']/g,
    (char) =>
      ({ '&': '&amp;', '"': '&quot;', '<': '&lt;', '>': '&gt;', "'": '&#39;' })[
        char
      ]!,
  );
}

export function webviewHtml(
  source: string,
  script: string,
  style: string,
  logo: string,
): string {
  const nonce = randomBytes(24).toString('base64');
  const csp = `default-src 'none'; img-src ${source}; style-src ${source}; script-src 'nonce-${nonce}'; connect-src 'none'; font-src ${source}; base-uri 'none'; form-action 'none';`;
  return `<!doctype html><html lang="en"><head><meta charset="UTF-8"><meta http-equiv="Content-Security-Policy" content="${escapeAttribute(csp)}"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Bugsnitch</title><link rel="stylesheet" href="${escapeAttribute(style)}"></head><body><div id="root" data-logo="${escapeAttribute(logo)}"></div><script nonce="${nonce}" src="${escapeAttribute(script)}"></script></body></html>`;
}
