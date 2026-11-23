import 'vuetify/styles'
import '@mdi/font/css/materialdesignicons.css'
import { createVuetify } from 'vuetify'

/** Mailroom tokens (FULL_SPECS.md §10.4). State colours are for state only. */
export const tokens = {
  light: {
    background: '#EDEFF1',
    surface: '#FAFBFB',
    inset: '#E2E6EA',
    ink: '#18222E',
    muted: '#566473',
    rule: '#CDD3DA',
    primary: '#2D6B6A',
    secondary: '#4A5FA3',
    success: '#3C7A4B',
    warning: '#B7860B',
    error: '#B3372F',
  },
  dark: {
    background: '#12171D',
    surface: '#1A222B',
    inset: '#0E1317',
    ink: '#E6EAEE',
    muted: '#9AA7B4',
    rule: '#2A3541',
    primary: '#5FB0AE',
    secondary: '#8FA2E0',
    success: '#6DBB80',
    warning: '#D9AD3F',
    error: '#E5776F',
  },
} as const

function theme(t: (typeof tokens)['light'] | (typeof tokens)['dark'], dark: boolean) {
  return {
    dark,
    colors: {
      background: t.background,
      surface: t.surface,
      'surface-variant': t.inset,
      'on-background': t.ink,
      'on-surface': t.ink,
      'on-surface-variant': t.muted,
      primary: t.primary,
      'on-primary': dark ? '#0A1F1F' : '#FFFFFF',
      secondary: t.secondary,
      'on-secondary': dark ? '#10162E' : '#FFFFFF',
      success: t.success,
      warning: t.warning,
      error: t.error,
      info: t.secondary,
      inset: t.inset,
    },
    variables: {
      'border-color': t.rule
        .replace('#', '')
        .match(/../g)!
        .map((h) => parseInt(h, 16))
        .join(','),
      'border-opacity': 1,
      'high-emphasis-opacity': 1,
      'medium-emphasis-opacity': 0.74,
    },
  }
}

const stored = localStorage.getItem('gas-theme')
const preferDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches

export default createVuetify({
  theme: {
    defaultTheme: stored ?? (preferDark ? 'dark' : 'light'),
    themes: {
      light: theme(tokens.light, false),
      dark: theme(tokens.dark, true),
    },
  },
  defaults: {
    global: { ripple: false },
    VBtn: { variant: 'text', rounded: 'md' },
    VCard: { variant: 'flat', border: true, rounded: 'lg' },
    VChip: { size: 'small', rounded: 'md' },
    VTextField: { variant: 'outlined', density: 'comfortable', color: 'primary' },
    VTooltip: { location: 'end' },
  },
})
