// SPDX-License-Identifier: MPL-2.0
import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { createRequire } from 'node:module';
import { rm } from 'node:fs/promises';
import type { ExtensionContext } from 'vscode';
import type { HostMessage } from '@bugsnitch/shared';
import {
  commitFile,
  createRepository,
  fixtureGit,
} from '../../../packages/git/test/fixture';

const require = createRequire(import.meta.url);
const host =
  require('../../../.cache/browser-host.cjs') as typeof import('../../../scripts/browser/host');
const dark = {
  '--vscode-foreground': '#cccccc',
  '--vscode-editor-background': '#1f1f1f',
  '--vscode-descriptionForeground': '#b7b7b7',
  '--vscode-button-foreground': '#ffffff',
  '--vscode-button-background': '#0067b8',
  '--vscode-button-secondaryBackground': '#383838',
  '--vscode-button-secondaryForeground': '#ffffff',
  '--vscode-panel-border': '#777777',
  '--vscode-focusBorder': '#3794ff',
  '--vscode-textCodeBlock-background': '#151515',
};

test('production host and webview complete an accessible investigation through controls', async ({
  page,
}) => {
  const root = await createRepository();
  await fixtureGit(root, 'config', 'user.name', 'Demo Developer');
  let panel: InstanceType<typeof host.BugsnitchPanel> | undefined;
  try {
    const good = await commitFile(
      root,
      'calculation.ts',
      'export const total = 30;\n',
      'Respect quantities',
    );
    const bad = await commitFile(
      root,
      'calculation.ts',
      'export const total = 10;\n',
      'Simplify total',
    );
    for (let i = 0; i < 11; i++)
      await commitFile(
        root,
        'docs.md',
        `Revision ${i}`,
        i === 10
          ? 'Documentation 10\n\n<img src=x onerror=window.__hostile=true>'
          : `Documentation ${i}`,
      );
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto('/');
    host.configure('http://127.0.0.1:4319', async (message: HostMessage) => {
      await page.evaluate(
        (data) => window.dispatchEvent(new MessageEvent('message', { data })),
        message,
      );
      return true;
    });
    await page.exposeFunction('sendToHost', (message: unknown) =>
      host.receiveMessage(message),
    );
    await page.evaluate(() => {
      Object.assign(window, {
        acquireVsCodeApi: () => ({
          postMessage: (message: unknown) =>
            (
              window as unknown as { sendToHost(message: unknown): void }
            ).sendToHost(message),
          getState: () => undefined,
          setState: () => {},
        }),
      });
    });
    panel = new host.BugsnitchPanel(
      { extensionUri: {} } as ExtensionContext,
      root,
      () => {},
    );
    await page.setContent(host.getHtml());
    await page.evaluate((variables) => {
      for (const [key, value] of Object.entries(variables))
        document.documentElement.style.setProperty(key, value);
    }, dark);
    await expect(
      page.getByRole('button', { name: 'Load more commits' }),
    ).toBeEnabled();
    await page.getByRole('button', { name: 'Load more commits' }).click();
    await page.getByRole('button', { name: /Simplify total/ }).click();
    await expect(
      page.getByRole('heading', { name: 'Simplify total', exact: true }),
    ).toBeFocused();
    await page.getByRole('button', { name: 'Mark known bad' }).click();
    await expect(
      page.getByRole('button', { name: /Inspect parent 1/ }),
    ).toBeEnabled();
    await page.getByRole('button', { name: /Inspect parent 1/ }).click();
    await page.getByRole('button', { name: 'Mark known good' }).click();
    await expect(
      page.getByRole('button', { name: 'Investigate range' }),
    ).toBeEnabled();
    await page.getByRole('button', { name: 'Investigate range' }).click();
    await expect(page.locator('.candidate-list')).toContainText(
      'Simplify total',
    );
    await page
      .locator('.candidate-list')
      .getByRole('button', { name: /Simplify total/ })
      .click();
    await page
      .getByLabel('Evidence and reproduction notes')
      .fill('Input quantity 3 returns 10 instead of 30.');
    await page.getByLabel('Your assessment').selectOption('confirmed');
    await page.getByRole('button', { name: 'Save assessment' }).click();
    await expect(
      page.getByText('Assessment saved in this panel.'),
    ).toBeVisible();
    await page
      .getByRole('button', { name: 'Preview patch for calculation.ts' })
      .click();
    await expect(
      page.getByRole('heading', { name: 'Patch · calculation.ts' }),
    ).toBeVisible();
    await expect(page.getByLabel('Commit patch')).toContainText(
      '+export const total = 10',
    );
    await page
      .getByRole('button', { name: 'File history for calculation.ts' })
      .click();
    await expect(
      page.getByRole('heading', { name: 'File history · calculation.ts' }),
    ).toBeVisible();
    await page.getByLabel('Find a loaded commit').fill('Simplify total');
    await expect(
      page.getByRole('region', { name: 'Loaded commit search results' }),
    ).toContainText('1 matches');
    await page.getByLabel('Find a loaded commit').fill('');
    await page.getByRole('button', { name: 'Copy commit hash' }).focus();
    await expect(
      page.getByRole('button', { name: 'Copy commit hash' }),
    ).toBeFocused();
    await page.keyboard.press('Enter');
    await page
      .getByRole('button', { name: 'Show in graph', exact: true })
      .click();
    await expect(page.locator(`#commit-${bad}`)).toBeFocused();
    expect(good).not.toBe(bad);
    await page
      .locator('.commit-list')
      .getByRole('button', { name: /Documentation 10/ })
      .click();
    await expect(page.locator('.message')).toContainText(
      '<img src=x onerror=window.__hostile=true>',
    );
    await expect(page.locator('.inspection img')).toHaveCount(0);
    expect(
      await page.evaluate(
        () => (window as unknown as { __hostile?: boolean }).__hostile,
      ),
    ).toBeUndefined();
    await page
      .locator('.commit-list')
      .getByRole('button', { name: /Simplify total/ })
      .click();
    const themes = [
      dark,
      {
        ...dark,
        '--vscode-foreground': '#202020',
        '--vscode-editor-background': '#ffffff',
        '--vscode-descriptionForeground': '#555555',
        '--vscode-panel-border': '#777777',
        '--vscode-textCodeBlock-background': '#eeeeee',
      },
      {
        ...dark,
        '--vscode-foreground': '#ffffff',
        '--vscode-editor-background': '#000000',
        '--vscode-descriptionForeground': '#ffffff',
        '--vscode-panel-border': '#ffffff',
        '--vscode-focusBorder': '#ffff00',
        '--vscode-button-background': '#000080',
        '--vscode-button-secondaryBackground': '#000000',
      },
    ];
    for (const theme of themes) {
      await page.evaluate((variables) => {
        for (const [key, value] of Object.entries(variables))
          document.documentElement.style.setProperty(key, value);
      }, theme);
      for (const width of [420, 1280]) {
        await page.setViewportSize({ width, height: 900 });
        const results = await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa'])
          .analyze();
        expect(results.violations).toEqual([]);
      }
    }
    await page.evaluate((variables) => {
      for (const [key, value] of Object.entries(variables))
        document.documentElement.style.setProperty(key, value);
    }, dark);
    // A repeated ready handshake replays loaded history and the evidence session.
    await host.receiveMessage({ version: 1, type: 'ready' });
    await expect(
      page.getByLabel('Evidence and reproduction notes'),
    ).toHaveValue('Input quantity 3 returns 10 instead of 30.');
    await expect(page.locator('.commit-list .commit-button')).toHaveCount(13);
    expect(errors).toEqual([]);
    await page.screenshot({
      path: 'test-results/investigation-dark.png',
      fullPage: true,
    });
  } finally {
    panel?.dispose();
    await rm(root, { recursive: true, force: true });
  }
});
