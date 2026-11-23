import { config } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import 'vuetify/styles'

// jsdom lacks these; Vuetify components touch them when mounting.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
}
window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as typeof window.matchMedia

config.global.plugins = [createVuetify()]
