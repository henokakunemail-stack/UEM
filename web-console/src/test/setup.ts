import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Unmount what each test rendered, so one test's DOM never satisfies the next
// test's query.
afterEach(() => {
  cleanup()
  localStorage.clear()
})
