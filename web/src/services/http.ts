/** RFC 9457 problem+json as returned by the API. */
export interface Problem {
  title?: string
  status?: number
  detail?: string
  errors?: { message?: string; location?: string }[]
}

export class ApiError extends Error {
  readonly status: number
  readonly problem?: Problem

  constructor(status: number, message: string, problem?: Problem) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.problem = problem
  }
}

/** The text a person should read for a problem response. */
export function problemMessage(problem: Problem | undefined, status: number): string {
  const detail = problem?.errors?.[0]?.message ?? problem?.detail ?? problem?.title
  if (detail) return detail
  if (status === 401) return 'Your session ended. Sign in again.'
  if (status >= 500) return 'The server had a problem. Try again in a moment.'
  return `Request failed (${status}).`
}

/**
 * Fetch wrapper used by the generated client (orval mutator) and by hand
 * written calls. Resolves with the parsed body, throws ApiError for non-2xx.
 * The generated functions expect the `{data, status, headers}` envelope, which
 * is what orval's fetch client builds around this.
 */
export async function http<T>(url: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')

  let res: Response
  try {
    res = await fetch(url, { ...init, headers, credentials: 'same-origin' })
  } catch {
    throw new ApiError(0, 'Could not reach the server. Check your connection and try again.')
  }

  const isJson = /json/.test(res.headers.get('content-type') ?? '')
  const text = res.status === 204 ? '' : await res.text()
  let body: unknown
  if (text && isJson) {
    try {
      body = JSON.parse(text)
    } catch {
      body = undefined
    }
  }
  if (!res.ok) {
    const problem = body as Problem | undefined
    throw new ApiError(res.status, problemMessage(problem, res.status), problem)
  }
  return { data: body, status: res.status, headers: res.headers } as T
}
