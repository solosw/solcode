import { resolve } from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const root = import.meta.dirname

export default defineConfig({
  plugins: [react(), tailwindcss()],
  root: resolve(root, 'src/renderer'),
  base: './',
  resolve: {
    alias: {
      '@': resolve(root, 'src/renderer'),
      '@shared': resolve(root, 'src/shared'),
    },
  },
  build: {
    outDir: resolve(root, 'dist/renderer'),
    emptyOutDir: true,
    target: 'es2023',
    sourcemap: true,
  },
})
