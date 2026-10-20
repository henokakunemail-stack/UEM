import { LANGS, useT } from './core'

/** EN / ID switch in the top bar. The choice is remembered per browser. */
export function LanguageToggle() {
  const { lang, setLang, t } = useT()
  return (
    <div className="lang-toggle" role="group" aria-label={t('common.language')}>
      {LANGS.map((l) => (
        <button
          key={l}
          type="button"
          className={`lang-toggle-btn ${lang === l ? 'active' : ''}`}
          aria-pressed={lang === l}
          onClick={() => setLang(l)}
          title={t(`common.language_${l}`)}
        >
          {l.toUpperCase()}
        </button>
      ))}
    </div>
  )
}
