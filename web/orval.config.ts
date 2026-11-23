import { defineConfig } from 'orval'

// Generates a typed client from the checked-in snapshot (openapi.json), so CI
// needs no running backend. Refresh the snapshot with `npm run snapshot:api`.
export default defineConfig({
  api: {
    input: './openapi.json',
    output: {
      mode: 'single',
      target: './src/api/generated.ts',
      client: 'fetch',
      clean: true,
      prettier: false,
      override: {
        mutator: { path: './src/services/http.ts', name: 'http' },
      },
    },
  },
})
