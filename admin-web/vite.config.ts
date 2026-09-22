import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:18089",
        headers: { host: "admin.localhost:18089" },
        // The dev browser is same-origin with Vite. The backend still checks
        // its own admin host/origin; this rewrite only exists in the dev proxy.
        configure: (proxy) => proxy.on("proxyReq", (request) => {
          request.setHeader("Origin", "http://admin.localhost:18089");
        }),
      },
    },
  },
});
