import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const rootDir = path.dirname(fileURLToPath(import.meta.url));

// The dev port is fixed rather than left to Vite to pick, since
// scripts/start.sh writes it straight to .yolocoder/web/port without
// having to parse it back out of Vite's own startup banner.
//
// The "@" alias also needs to be declared here, not just in
// tsconfig.json: the tsconfig path only satisfies the type checker, but
// Vite resolves imports at runtime through its own config.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(rootDir, "src"),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": "http://localhost:3001",
    },
  },
});
