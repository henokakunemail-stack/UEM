import { createContext, useContext, useState, type ReactNode } from 'react'
import { Moon, Sun } from 'lucide-react'

type Theme = 'light' | 'dark'
const ThemeContext = createContext<{ theme: Theme; toggle: () => void } | null>(null)

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(() => document.documentElement.dataset.theme === 'light' ? 'light' : 'dark')
  const toggle = () => {
    const next = theme === 'light' ? 'dark' : 'light'
    document.documentElement.dataset.theme = next
    try { localStorage.setItem('em_theme', next) } catch { /* Theme still works without storage. */ }
    setTheme(next)
  }
  return <ThemeContext.Provider value={{ theme, toggle }}>{children}</ThemeContext.Provider>
}

export function ThemeToggle() {
  const context = useContext(ThemeContext)
  if (!context) throw new Error('ThemeToggle requires ThemeProvider')
  const { theme, toggle } = context
  const label = `Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`
  return <button type="button" className="btn-icon theme-toggle" onClick={toggle} aria-label={label} title={label}>
    {theme === 'dark' ? <Sun size={17} /> : <Moon size={17} />}
  </button>
}
