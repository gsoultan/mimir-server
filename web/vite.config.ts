import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { VitePWA } from "vite-plugin-pwa";

// API paths the dev server forwards to the Go server, and the service
// worker must never answer from its cache.
const api = ["/mimir.v1.", "/grpc.", "/api/"];

// Where the dev server finds the Go server; scripts/dev.sh sets it.
const apiTarget = process.env.MIMIR_API ?? "http://localhost:8080";

export default defineConfig({
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    tailwindcss(),
    VitePWA({
      registerType: "prompt",
      includeAssets: ["icon.svg"],
      manifest: {
        name: "mimir",
        short_name: "mimir",
        description: "Run and share your mimir models.",
        theme_color: "#0E6B5C",
        background_color: "#F4F6F3",
        display: "standalone",
        start_url: "/",
        icons: [{ src: "icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any maskable" }],
      },
      workbox: {
        globPatterns: ["**/*.{js,css,html,svg,woff2}"],
        navigateFallback: "/index.html",
        navigateFallbackDenylist: api.map((p) => new RegExp("^" + p.replaceAll(".", "\\."))),
      },
    }),
  ],
  server: {
    proxy: Object.fromEntries(api.map((p) => [p, apiTarget])),
  },
  worker: { format: "es" },
  build: { target: "es2024" },
});
