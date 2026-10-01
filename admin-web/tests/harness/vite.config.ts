import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Builds the CSP smoke test's component harness; it is never part of dist/.
export default defineConfig({
  root: fileURLToPath(new URL(".", import.meta.url)),
  base: "/harness/",
  plugins: [react(), tailwindcss()],
  build: { emptyOutDir: true, chunkSizeWarningLimit: 2000, assetsInlineLimit: (file) => /\.(woff2?|ttf|otf)$/.test(file) ? false : undefined },
});
