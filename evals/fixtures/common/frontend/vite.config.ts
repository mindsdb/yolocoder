import { defineConfig } from 'vite';
import { fileURLToPath } from 'node:url';
export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  cacheDir: fileURLToPath(new URL('../.yolocoder/vite-cache', import.meta.url)),
  server: { host: '127.0.0.1', port: Number(process.env.FRONTEND_PORT), strictPort: true,
    proxy: { '/api': `http://127.0.0.1:${process.env.BACKEND_PORT}` } },
});
