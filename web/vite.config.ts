import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: './',
  build: { outDir: '../cmd/server/dist', emptyOutDir: true },
  server: { proxy: { '/api': 'http://localhost:8084', '/logos': 'http://localhost:8084' } },
})
