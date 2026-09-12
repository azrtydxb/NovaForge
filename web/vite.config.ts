import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies the API to the edge so the app is developed against
// a real backend rather than a fixture. NOVAFORGE_EDGE points it at a
// deployment; the default is a port-forward of the in-cluster edge.
const edge = process.env.NOVAFORGE_EDGE ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": { target: edge, changeOrigin: true } },
  },
  build: { outDir: "dist", sourcemap: true },
});
