import type { UserDTO } from '../types/api'

export interface UserEdit {
  display_name?: string
  role?: string
}

/**
 * The fields an edit actually changes. The editor used to send role with
 * every save, and the server treated any role in the body as a role change,
 * so renaming a user signed them out of every session. Sending only what
 * changed keeps an edit to what the operator did.
 */
export function changedUserFields(original: UserDTO, edited: { display_name: string; role: string }): UserEdit {
  const out: UserEdit = {}
  if (edited.display_name !== (original.display_name || '')) out.display_name = edited.display_name
  if (edited.role !== original.role) out.role = edited.role
  return out
}

/** The server's refusals for user changes, mapped to i18n keys. */
export const USER_ERRORS: Record<string, string> = {
  last_admin: 'users.error_last_admin',
  cannot_deactivate_self: 'users.error_cannot_deactivate_self',
  cannot_change_own_role: 'users.error_cannot_change_own_role',
}
