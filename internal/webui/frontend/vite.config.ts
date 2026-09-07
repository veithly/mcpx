import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
export default defineConfig({
  base: '/mcp/app/', plugins: [react()],
  server: { proxy: { '/mcp/app/api': 'http://127.0.0.1:9090' } },
  build: { outDir: 'dist', sourcemap: false },
});
