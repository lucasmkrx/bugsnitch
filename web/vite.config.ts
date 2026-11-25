import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

export default defineConfig({
  build: {
    rollupOptions: {
      input: {
        homepage: fileURLToPath(new URL("./index.html", import.meta.url)),
        workspace: fileURLToPath(new URL("./app/index.html", import.meta.url)),
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: { "/api": { target: "http://127.0.0.1:8787" } },
  },
});
