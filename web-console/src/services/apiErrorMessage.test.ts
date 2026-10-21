import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import { apiErrorMessage } from './apiErrorMessage'

const t = (key: string, vars?: Record<string, string | number>) => (vars && 'seconds' in vars ? `${key}:${vars.seconds}` : key)

describe('apiErrorMessage', () => {
  const map = { last_admin: 'users.error_last_admin', too_many_attempts: 'auth.error_too_many_attempts' }

  it('maps a known code to its key, with retryAfter as seconds', () => {
    expect(apiErrorMessage(new ApiError('x', 409, 'last_admin'), t, map, 'fallback')).toBe('users.error_last_admin:0')
    expect(apiErrorMessage(new ApiError('x', 429, 'too_many_attempts', 42), t, map, 'fallback')).toBe('auth.error_too_many_attempts:42')
  })

  it('falls back for unknown codes, missing codes and plain errors', () => {
    expect(apiErrorMessage(new ApiError('x', 500, 'something_else'), t, map, 'fallback')).toBe('fallback')
    expect(apiErrorMessage(new ApiError('x', 500), t, map, 'fallback')).toBe('fallback')
    expect(apiErrorMessage(new Error('boom'), t, map, 'fallback')).toBe('fallback')
  })
})
