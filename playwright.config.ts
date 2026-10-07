// SPDX-License-Identifier: MPL-2.0
import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: 'apps/vscode/browser-tests',
  workers: 1,
  timeout: 60000,
  use: {
    baseURL: 'http://127.0.0.1:4319',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video:
      process.env.BUGSNITCH_RECORD_DEMO === '1' ? 'on' : 'retain-on-failure',
  },
  reporter: [['list'], ['html', { open: 'never' }]],
  webServer: {
    command: 'node scripts/browser-server.mjs',
    url: 'http://127.0.0.1:4319',
    reuseExistingServer: false,
  },
});
