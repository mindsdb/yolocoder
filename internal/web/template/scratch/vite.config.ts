import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev port is fixed rather than left to Vite to pick, since
// scripts/start.sh writes it straight to .yolocoder/web/port without
// having to parse it back out of Vite's own startup banner.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": "http://localhost:3001",
    },
  },
});
