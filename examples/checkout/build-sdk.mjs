// Run from this directory after installing the two pinned development dependencies.
import { readFile } from "node:fs/promises"
import { build } from "esbuild"
const { version } = JSON.parse(await readFile("node_modules/@sentry/browser/package.json", "utf8"))
if (version !== "11.13.0") throw new Error("This fixture expects @sentry/browser 11.13.0")
await build({
  stdin: { contents: 'export { init, captureException, flush } from "@sentry/browser"', resolveDir: process.cwd() },
  bundle: true, format: "esm", platform: "browser", outfile: "sentry-sdk.mjs",
})
