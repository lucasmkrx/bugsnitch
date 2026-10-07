// SPDX-License-Identifier: MPL-2.0
import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import prettier from 'eslint-config-prettier';
import globals from 'globals';

export default tseslint.config(
  {
    ignores: [
      '**/dist/**',
      '**/node_modules/**',
      '**/coverage/**',
      '**/.cache/**',
      '.vscode-test/**',
      'playwright-report/**',
      'test-results/**',
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  { languageOptions: { globals: { ...globals.node, ...globals.browser } } },
  {
    files: [
      'packages/{shared,graph,ui}/src/**/*.{ts,tsx}',
      'apps/vscode/webview/**/*.{ts,tsx}',
    ],
    ignores: ['**/*.test.ts', '**/*.test.tsx'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            'node:*',
            'vscode',
            '@bugsnitch/git',
            '@bugsnitch/forensics',
            '**/git/src/**',
            '**/forensics/src/**',
          ],
        },
      ],
    },
  },
  {
    files: ['packages/{git,forensics}/src/**/*.ts'],
    ignores: ['**/*.test.ts', '**/*.test.tsx'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            'vscode',
            '@bugsnitch/ui',
            'react',
            'react-dom',
            '**/apps/vscode/**',
          ],
        },
      ],
    },
  },
  prettier,
);
