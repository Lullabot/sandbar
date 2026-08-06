import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'path';

// This app never imports @self-review/core in the browser — that package
// carries Node-only dependencies (fs, child_process) and is the server's
// job (server/index.mjs). Unlike upstream's tests/webapp/vite.config.ts,
// there is deliberately no alias pointing @self-review/core at a browser
// build.
export default defineConfig({
  root: path.resolve(__dirname),
  base: '/',
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
