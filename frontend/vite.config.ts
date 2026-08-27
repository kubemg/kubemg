/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

/*
 * The dev server proxies the backend so the console is reachable from another
 * machine over a single port: the browser only ever talks to the origin it
 * loaded the page from, which is what makes external access work without baking
 * a hostname into the bundle. ws:true is required — the in-page terminal and
 * followed logs are WebSocket upgrades under /api.
 */
const apiTarget = process.env.VITE_DEV_PROXY_TARGET ?? 'http://localhost:8080'

/*
 * The bastion has to serve HTTPS for kubectl to work against it at all, and a
 * dev stack's certificate is self-signed. This is the proxy's own hop to the
 * backend, not the browser's, so accepting it here is scoped to the dev server
 * — the browser still sees whatever certificate the console is served under.
 */
const proxySecure = process.env.VITE_DEV_PROXY_INSECURE !== 'true'

/*
 * Vite rejects a Host header it does not recognise, which is why reaching the
 * dev server by hostname 404s where an IP works. Name the hostnames the console
 * is served under in VITE_ALLOWED_HOSTS; unset accepts any, which is fine for a
 * dev stack but should be narrowed anywhere it is exposed for real.
 */
const allowedHosts = process.env.VITE_ALLOWED_HOSTS
  ? process.env.VITE_ALLOWED_HOSTS.split(',').map((host) => host.trim()).filter(Boolean)
  : true

/*
 * The test runner is Vite's own, so a test resolves a module exactly the way the
 * bundle does and there is no second build to keep in step.
 *
 * The default environment is `node`, not `jsdom`: most of what is worth
 * asserting here is a pure function (`lib/objectForm.ts` renders a manifest,
 * `lib/resources.ts` derives the sidebar, `lib/insights.ts` buckets pods), and a
 * DOM none of them touch is startup cost per run for nothing. A file that does
 * need one asks for it in its own docblock — `@vitest-environment jsdom`.
 */
// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  test: {
    environment: 'node',
    include: ['src/**/*.test.{ts,tsx}'],
  },
  server: {
    host: true,
    allowedHosts,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true, ws: true, secure: proxySecure },
      '/health': { target: apiTarget, changeOrigin: true, secure: proxySecure },
      // The agent tunnel and the unauthenticated install package, so a target
      // cluster can be pointed at the address an operator already uses.
      '/agent': { target: apiTarget, changeOrigin: true, ws: true, secure: proxySecure },
      '/install': { target: apiTarget, changeOrigin: true, secure: proxySecure },
    },
  },
})
