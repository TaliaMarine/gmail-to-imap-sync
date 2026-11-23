import { onMounted, ref, type Ref } from 'vue'
import { ApiError } from '../services/http'

export interface Loader<T> {
  data: Ref<T | undefined>
  error: Ref<string>
  loading: Ref<boolean>
  reload: () => Promise<void>
}

/** Text to show for any thrown value. */
export function errorText(e: unknown, fallback = 'Something went wrong. Try again.'): string {
  if (e instanceof ApiError) return e.message
  return e instanceof Error && e.message ? e.message : fallback
}

/** Loads on mount and on demand; keeps the old data visible while reloading. */
export function useLoader<T>(fn: () => Promise<T>, opts: { immediate?: boolean } = {}): Loader<T> {
  const data = ref<T>() as Ref<T | undefined>
  const error = ref('')
  const loading = ref(false)
  let seq = 0

  async function reload() {
    const mine = ++seq
    loading.value = true
    error.value = ''
    try {
      const r = await fn()
      if (mine === seq) data.value = r
    } catch (e) {
      if (mine === seq) error.value = errorText(e, 'Could not load this page.')
    } finally {
      if (mine === seq) loading.value = false
    }
  }
  if (opts.immediate !== false) onMounted(reload)
  return { data, error, loading, reload }
}
