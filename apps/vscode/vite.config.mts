// SPDX-License-Identifier: MPL-2.0
import { defineConfig } from 'vite';

export default defineConfig({
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  build: {
    outDir: 'dist/webview',
    lib: {
      entry: 'webview/main.tsx',
      name: 'Bugsnitch',
      formats: ['iife'],
      fileName: () => 'webview.js',
      cssFileName: 'webview',
    },
    sourcemap: false,
  },
});
