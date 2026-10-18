import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    // Same-origin API in development, like nginx does in production
    proxy: {
      "/api": process.env.API_UPSTREAM ?? "http://127.0.0.1:8080",
    },
  },
})
