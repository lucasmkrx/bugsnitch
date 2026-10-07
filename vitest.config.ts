// SPDX-License-Identifier: MPL-2.0
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    include: ['packages/**/*.test.ts', 'apps/**/*.test.{ts,tsx}'],
    testTimeout: 15000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'json-summary'],
      include: ['packages/*/src/**/*.ts', 'apps/vscode/src/**/*.ts'],
      exclude: ['**/*.test.ts'],
      thresholds: { lines: 85, statements: 85, branches: 75, functions: 80 },
    },
  },
});
