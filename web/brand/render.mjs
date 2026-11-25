// SPDX-License-Identifier: MPL-2.0
// Render the browser and social assets from the approved icon and webfont.
import { readFile, writeFile } from "node:fs/promises";
import { chromium } from "playwright-core";

const output = new URL("../public/brand/", import.meta.url);
const icon = await readFile(new URL("bugsnitch-logo.svg", output), "utf8");
const wordmark = await readFile(new URL("bugsnitch-wordmark.svg", output));
const bold = await readFile(
  new URL("../src/fonts/ibm-plex-mono-latin-700.woff2", import.meta.url),
);
const dataURL = (mime, bytes) =>
  `data:${mime};base64,${bytes.toString("base64")}`;

const art = icon
  .match(/<path\b[^>]*\/>/g)
  .join("\n    ")
  .replace('class="cls-1"', 'fill="#d40000"');

function tile(markWidth, corner) {
  const scale = markWidth / 507.85;
  const x = (512 - markWidth) / 2;
  const y = (512 - 442.43 * scale) / 2;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
  <rect width="512" height="512" rx="${corner}" fill="#faf9f6"/>
  <g transform="translate(${x} ${y}) scale(${scale})">
    ${art}
  </g>
</svg>`;
}

function ico(images) {
  const directory = Buffer.alloc(6 + 16 * images.length);
  directory.writeUInt16LE(1, 2);
  directory.writeUInt16LE(images.length, 4);
  let offset = directory.length;
  for (const [index, { size, png }] of images.entries()) {
    const entry = 6 + index * 16;
    directory[entry] = directory[entry + 1] = size;
    directory.writeUInt16LE(1, entry + 4);
    directory.writeUInt16LE(32, entry + 6);
    directory.writeUInt32LE(png.length, entry + 8);
    directory.writeUInt32LE(offset, entry + 12);
    offset += png.length;
  }
  return Buffer.concat([directory, ...images.map(({ png }) => png)]);
}

const browser = await chromium.launch(
  process.env.BUGSNITCH_TEST_CHROME
    ? { executablePath: process.env.BUGSNITCH_TEST_CHROME }
    : { channel: "chrome" },
);
try {
  async function capture(width, height, markup) {
    const page = await browser.newPage({ viewport: { width, height } });
    try {
      await page.setContent(
        `<!doctype html><style>body{margin:0}</style>${markup}`,
      );
      await page.evaluate(() => document.fonts.ready);
      return await page.screenshot({
        omitBackground: true,
        clip: { x: 0, y: 0, width, height },
      });
    } finally {
      await page.close();
    }
  }
  const scaled = (svg, width, height) =>
    `<img alt="" width="${width}" height="${height}" src="${dataURL("image/svg+xml", svg)}">`;
  const favicon = tile(397, 88);
  await writeFile(new URL("favicon.svg", output), favicon + "\n");
  const embedded = [];
  for (const size of [16, 32, 48]) {
    const png = await capture(
      size,
      size,
      scaled(Buffer.from(favicon), size, size),
    );
    await writeFile(new URL(`favicon-${size}.png`, output), png);
    embedded.push({ size, png });
  }
  await writeFile(new URL("../favicon.ico", output), ico(embedded));

  const profile = Buffer.from(tile(330, 0));
  for (const [name, size] of [
    ["apple-touch-icon.png", 180],
    ["app-icon-192.png", 192],
    ["app-icon-512.png", 512],
  ]) {
    await writeFile(
      new URL(name, output),
      await capture(size, size, scaled(profile, size, size)),
    );
  }
  await writeFile(
    new URL("bugsnitch-wordmark.png", output),
    await capture(748, 140, scaled(wordmark, 748, 140)),
  );

  const share = await capture(
    1200,
    630,
    `<style>
      @font-face {font-family:Plex; src:url(${dataURL("font/woff2", bold)}); font-weight:700}
      body {background:#faf9f6; color:#201e1d}
      main {height:100vh; display:flex; flex-direction:column; align-items:center; justify-content:center; gap:32px}
      h1 {margin:0; font:700 76px/1.1 Plex,monospace}
     </style>
     <main>
       ${scaled(Buffer.from(icon), 256, 223)}
       <h1>Bugsnitch</h1>
     </main>`,
  );
  await writeFile(new URL("social-share.png", output), share);
} finally {
  await browser.close();
}
