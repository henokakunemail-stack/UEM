import { ApiError } from './api'

type Translate = (key: string, vars?: Record<string, string | number>) => string

/**
 * Turns a failed API call into a message in the operator's language. A known
 * error code maps to its i18n key (with retryAfter available as {seconds});
 * anything else falls back to fallbackKey rather than showing the server's
 * English text.
 */
export function apiErrorMessage(err: unknown, t: Translate, map: Record<string, string>, fallbackKey: string): string {
  if (err instanceof ApiError && err.code && map[err.code]) {
    return t(map[err.code], { seconds: err.retryAfter ?? 0 })
  }
  return t(fallbackKey)
}
