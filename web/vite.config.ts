import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import vuetify from 'vite-plugin-vuetify'

const proxy = {
  '/api': 'http://localhost:1080',
  '/auth': 'http://localhost:1080',
  '/oauth': 'http://localhost:1080',
}

export default defineConfig({
  plugins: [vue(), vuetify({ autoImport: true })],
  server: { port: 3000, proxy },
  preview: { port: 3000, proxy },
})
