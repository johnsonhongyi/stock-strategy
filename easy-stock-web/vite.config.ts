import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  base: './',
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 20073,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:20081',
        // Keep Host aligned with Origin for tokenless same-origin linkage setup.
        changeOrigin: false,
        ws: true,
      },
    },
  },
});
