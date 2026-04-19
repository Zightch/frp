import path from "node:path";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

const managementTarget = process.env.VITE_MANAGEMENT_API_TARGET ?? "http://127.0.0.1:7500";

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    host: "0.0.0.0",
    port: 5173,
    proxy: {
      "/api": {
        target: managementTarget,
        changeOrigin: true,
      },
      "/healthz": {
        target: managementTarget,
        changeOrigin: true,
      },
      "/readyz": {
        target: managementTarget,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
