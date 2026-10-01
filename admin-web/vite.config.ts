import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    // The admin CSP has no data: in font-src, so fonts must stay separate files; small images may still inline because img-src allows data:.
    assetsInlineLimit: (file) => /\.(woff2?|ttf|otf)$/.test(file) ? false : undefined,
    rolldownOptions: {
      output: {
        // React itself changes rarely, so it gets a long-lived chunk of its own; libraries used only by lazy pages (recharts, react-table) stay in those pages' chunks.
        // Zod shares a chunk with src/zod-config.ts so jitless mode is set when that chunk evaluates, before any other chunk can build an object schema; otherwise a shared chunk that defines schemas at module level runs ahead of main.tsx's import and Zod's eval probe trips the CSP.
        codeSplitting: { groups: [{ name: "react", test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/ }, { name: "zod", test: /node_modules[\\/]zod[\\/]|[\\/]src[\\/]zod-config\.ts$/ }] },
      },
    },
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:18089",
        headers: { host: "admin.localhost:18089" },
        // The dev browser is same-origin with Vite. The backend still checks its own admin host/origin; this rewrite only exists in the dev proxy.
        configure: (proxy) => proxy.on("proxyReq", (request) => {
          request.setHeader("Origin", "http://admin.localhost:18089");
        }),
      },
    },
  },
});
