import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
export default defineConfig({
  plugins: [react()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    cors: false,
    fs: { strict: true, allow: [fileURLToPath(new URL(".", import.meta.url))] },
    // The browser reaches Vite only through the TemplateContext origin, so the
    // hot-reload WebSocket uses that port (also through a same-port forward).
    ws: {
      path: "/__ui_hmr",
      clientPort: Number(process.env.TEMPLATECONTEXT_DEV_PORT || 8098),
    },
  },
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
