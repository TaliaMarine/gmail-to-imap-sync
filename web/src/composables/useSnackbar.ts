import { reactive } from 'vue'

export const snack = reactive({ show: false, text: '', color: 'success' as 'success' | 'error' | 'info' })

/** Confirms an action with the same verb the button used ("Saved", "Retried"). */
export function notify(text: string, color: 'success' | 'error' | 'info' = 'success') {
  snack.text = text
  snack.color = color
  snack.show = true
}
