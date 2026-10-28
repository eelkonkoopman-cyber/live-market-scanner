import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Built to dist/, which the Go binary embeds via //go:embed. base './' keeps
// asset URLs relative so they resolve under the embedded file server.
// dev: forward the WS to the Go backend. Defaults to 7878 (the normal local
// scanner.exe port); override with SCANNER_WS_PORT to point at an isolated
// preview instance (e.g. the .claude/launch.json "scanner" config on 7900)
// instead of a real running instance.
const backendPort = process.env.SCANNER_WS_PORT || '7878';

export default defineConfig({
  plugins: [react()],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      '/stream': { target: `ws://localhost:${backendPort}`, ws: true },
    },
  },
});
