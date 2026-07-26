import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const apiTarget = process.env.MMWX_API_TARGET;

export default defineConfig({
  plugins: [react()],
  server: apiTarget
    ? {
        proxy: {
          "/api": {
            target: apiTarget,
            changeOrigin: true,
            secure: true,
            ws: true,
          },
        },
      }
    : undefined,
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ["react", "react-dom"],
          charts: ["recharts"],
        },
      },
    },
  },
});
