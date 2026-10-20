import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { I18nContext, translate, type I18nValue, type Lang } from './core'

const STORAGE_KEY = 'em_lang'

function initialLang(): Lang {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'en' || stored === 'id') return stored
  } catch {
    /* Storage blocked: fall back to the browser language. */
  }
  return typeof navigator !== 'undefined' && navigator.language?.toLowerCase().startsWith('id') ? 'id' : 'en'
}

export function I18nProvider({ children, lang: forced }: { children: ReactNode; lang?: Lang }) {
  const [lang, setLangState] = useState<Lang>(() => forced ?? initialLang())
  useEffect(() => {
    document.documentElement.lang = lang
  }, [lang])
  const setLang = useCallback((next: Lang) => {
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      /* The choice still applies for this visit. */
    }
    setLangState(next)
  }, [])
  const value = useMemo<I18nValue>(() => ({ lang, setLang, t: (key, vars) => translate(lang, key, vars) }), [lang, setLang])
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

