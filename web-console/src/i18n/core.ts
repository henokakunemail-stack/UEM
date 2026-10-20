import { createContext, useContext } from 'react'

export type Lang = 'en' | 'id'
export const LANGS: Lang[] = ['en', 'id']

/** Every locales/<lang>/<namespace>.json file, merged into one flat table per
 *  language with keys "<namespace>.<key>". A page owns its own namespace file,
 *  so adding a page never means editing a shared dictionary. */
const files = import.meta.glob('./locales/*/*.json', { eager: true, import: 'default' }) as Record<
  string,
  Record<string, string>
>

function build(lang: Lang): Record<string, string> {
  const table: Record<string, string> = {}
  for (const [path, entries] of Object.entries(files)) {
    const m = /\.\/locales\/([^/]+)\/([^/]+)\.json$/.exec(path)
    if (!m || m[1] !== lang) continue
    for (const [key, value] of Object.entries(entries)) table[`${m[2]}.${key}`] = value
  }
  return table
}

export const dictionaries: Record<Lang, Record<string, string>> = { en: build('en'), id: build('id') }

export type Vars = Record<string, string | number>

/** Looks a key up in lang, then English, then returns the key itself -- so a
 *  missing translation shows English rather than a blank, and a missing key is
 *  visible on screen instead of silently empty. {name} placeholders are filled
 *  from vars. */
export function translate(lang: Lang, key: string, vars?: Vars): string {
  const text = dictionaries[lang][key] ?? dictionaries.en[key] ?? key
  if (!vars) return text
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in vars ? String(vars[name]) : whole))
}

export interface I18nValue {
  lang: Lang
  setLang: (lang: Lang) => void
  t: (key: string, vars?: Vars) => string
}

export const I18nContext = createContext<I18nValue | null>(null)

/** t() for the current language. Outside a provider (a component rendered in
 *  isolation by a test) it falls back to English rather than throwing. */
export function useT(): I18nValue {
  const ctx = useContext(I18nContext)
  if (ctx) return ctx
  return { lang: 'en', setLang: () => {}, t: (key, vars) => translate('en', key, vars) }
}
