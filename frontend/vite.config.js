import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Мини-апп раздаётся под путём /commenter/ (за nginx, который срезает префикс и
// проксирует на Go-бэкенд :8090). base влияет и на dev, и на build.
export default defineConfig({
  base: '/commenter/',
  plugins: [vue()],
  server: {
    proxy: {
      // в деве /commenter/api → бэкенд :8090 (убираем префикс)
      '/commenter/api': {
        target: 'http://localhost:8090',
        rewrite: (p) => p.replace(/^\/commenter/, ''),
      },
    },
  },
  build: { outDir: 'dist' },
})
