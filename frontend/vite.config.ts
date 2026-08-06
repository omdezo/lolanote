import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev server proxies API + WebSocket traffic to the Go backend so the SPA
// and API share an origin in development.
export default defineConfig({
  plugins: [react()],
  // Component tests render for real, in a DOM. renderToString cannot stand in
  // for it: zustand 5 hands React getInitialState as the server snapshot, so a
  // server render sees the store as it was at boot and every selector-driven
  // component draws itself empty — which is a false pass on exactly the
  // components most worth checking.
  test: {
    environment: 'jsdom',
    environmentMatchGlobs: [['**/*.test.ts', 'node']],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/ws': { target: 'ws://localhost:8080', ws: true },
      // Keycloak on the dev origin too, mirroring what nginx does in the
      // container. The client resolves its auth server from window.location,
      // so without these the dev server would look for /realms on :5173 and
      // find nothing.
      '/realms': { target: 'http://localhost:8081', changeOrigin: true },
      '/resources': { target: 'http://localhost:8081', changeOrigin: true },
    },
  },
});
