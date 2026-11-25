// SPDX-License-Identifier: MPL-2.0
// The demo is a separate loopback process. Its fixed records cannot contact any external service.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { dirname, extname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const dist = resolve(dirname(fileURLToPath(import.meta.url)), "../dist");
const data = await readFile(new URL("./workspace.json", import.meta.url));
const index = await readFile(resolve(dist, "app/index.html"));
const types = {
  ".html": "text/html",
  ".css": "text/css",
  ".js": "text/javascript",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
};
const previewIcons = new Set([
  "/favicon.ico",
  "/brand/favicon.svg",
  "/brand/bugsnitch-logo.svg",
  "/brand/apple-touch-icon.png",
]);
const port = 4174;
const origin = "http://127.0.0.1:" + port;
createServer(async (req, res) => {
  res.setHeader("Cache-Control", "no-store");
  res.setHeader("X-Content-Type-Options", "nosniff");
  res.setHeader("Referrer-Policy", "no-referrer");
  res.setHeader(
    "Content-Security-Policy",
    "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'",
  );
  if (req.headers.host !== "127.0.0.1:" + port || req.method !== "GET") {
    res
      .writeHead(403, { "Content-Type": "application/json" })
      .end(JSON.stringify({ error: "Read-only local demo" }));
    return;
  }
  try {
    const pathname = new URL(req.url, origin).pathname;
    if (pathname === "/api/v1/state") {
      res.writeHead(200, { "Content-Type": "application/json" }).end(data);
    } else if (pathname === "/") {
      res.writeHead(200, { "Content-Type": "text/html" }).end(index);
    } else if (
      (previewIcons.has(pathname) ||
        /^\/assets\/[A-Za-z0-9._-]+$/.test(pathname)) &&
      types[extname(pathname)]
    ) {
      const bytes = await readFile(resolve(dist, "." + pathname));
      res
        .writeHead(200, { "Content-Type": types[extname(pathname)] })
        .end(bytes);
    } else {
      res.writeHead(404).end();
    }
  } catch {
    res.writeHead(404).end();
  }
}).listen(port, "127.0.0.1", () =>
  console.log("Open " + origin + " for the read-only Bugsnitch demo."),
);
