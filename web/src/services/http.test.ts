import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, http, problemMessage } from './http'

function respond(status: number, body?: unknown, type = 'application/problem+json') {
  vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => new Response(body === undefined ? null : JSON.stringify(body), { status, headers: type ? { 'content-type': type } : {} })))
}
afterEach(() => vi.unstubAllGlobals())

describe('http', () => {
  it('wraps a successful JSON body with its status', async () => {
    respond(200, { ok: true }, 'application/json')
    const r = await http<{ data: { ok: boolean }; status: number }>('/x')
    expect(r.data).toEqual({ ok: true })
    expect(r.status).toBe(200)
  })

  it('handles 204 without a body', async () => {
    respond(204)
    const r = await http<{ data: unknown; status: number }>('/x', { method: 'DELETE' })
    expect(r.status).toBe(204)
    expect(r.data).toBeUndefined()
  })

  it('throws ApiError carrying the first validation message', async () => {
    respond(422, { title: 'Unprocessable Entity', detail: 'validation failed', errors: [{ message: 'expected length >= 1', location: 'body.name' }] })
    await expect(http('/x', { method: 'POST', body: '{}' })).rejects.toMatchObject({ name: 'ApiError', status: 422, message: 'expected length >= 1' })
  })

  it('falls back to detail, then title', async () => {
    respond(409, { title: 'Conflict', detail: 'The destination is used by 2 accounts.' })
    await expect(http('/x')).rejects.toThrow('The destination is used by 2 accounts.')
    respond(409, { title: 'Conflict' })
    await expect(http('/x')).rejects.toThrow('Conflict')
  })

  it('gives plain messages when the server sends no problem body', () => {
    expect(problemMessage(undefined, 401)).toContain('Sign in again')
    expect(problemMessage(undefined, 502)).toContain('server had a problem')
    expect(problemMessage(undefined, 404)).toBe('Request failed (404).')
  })

  it('reports an unreachable server', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const err = (await http('/x').catch((e) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(0)
    expect(err.message).toContain('Could not reach the server')
  })

  it('sends JSON content type only with a body', async () => {
    respond(200, {}, 'application/json')
    await http('/x', { method: 'POST', body: '{}' })
    const init = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0]![1] as RequestInit
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
    await http('/y')
    const init2 = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[1]![1] as RequestInit
    expect(new Headers(init2.headers).has('Content-Type')).toBe(false)
  })
})
