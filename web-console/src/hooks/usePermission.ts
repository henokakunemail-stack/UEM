import { useAuth } from '../context/AuthContext'

const ranks = { viewer: 1, technician: 2, admin: 3 } as const

export function usePermission() {
  const { user } = useAuth()
  const rank = ranks[user?.rol as keyof typeof ranks] ?? 0
  return { can: (role: keyof typeof ranks) => rank >= ranks[role] }
}
