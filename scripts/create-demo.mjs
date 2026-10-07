// SPDX-License-Identifier: MPL-2.0
import { createDemoFixture } from './demo-fixture.mjs';

const fixture = await createDemoFixture();
console.log(`Demo repository: ${fixture.root}`);
console.log(
  'Open this folder in VS Code, then investigate line 2 of checkout.js.',
);
console.log(
  'This is a synthetic, disposable example; your checkout is unchanged.',
);
