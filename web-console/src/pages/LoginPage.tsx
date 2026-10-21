import { useState, type FormEvent } from 'react'
import { AlertCircle, ArrowRight, ChartNoAxesCombined, Layers, LockKeyhole, Monitor, ShieldCheck, User } from 'lucide-react'
import { useAuth } from '../context/AuthContext'
import { ThemeToggle } from '../context/ThemeContext'
import { LanguageToggle } from '../i18n/LanguageToggle'
import { useT } from '../i18n/core'
import { apiErrorMessage } from '../services/apiErrorMessage'

// The server's reasons, shown in the operator's language. Any other failure
// (network, 500) falls back to a generic message rather than raw server text.
const LOGIN_ERRORS: Record<string, string> = {
  invalid_credentials: 'auth.error_invalid_credentials',
  account_disabled: 'auth.error_account_disabled',
  too_many_attempts: 'auth.error_too_many_attempts',
}

export function LoginPage() {
  const { login } = useAuth()
  const { t } = useT()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setError(null)
    setLoading(true)
    try { await login(username, password) }
    catch (error) { setError(apiErrorMessage(error, t, LOGIN_ERRORS, 'auth.unable_to_sign_in')) }
    finally { setLoading(false) }
  }

  return <div className="login-screen">
    <div className="login-theme" style={{ display: 'flex', alignItems: 'center', gap: 8 }}><LanguageToggle /><ThemeToggle /></div>
    <div className="login-layout">
      <section className="login-story" aria-labelledby="product-title">
        <div className="login-wordmark"><Monitor size={23} /><span>{t('auth.product_name')}</span></div>
        <span className="eyebrow">{t('auth.eyebrow')}</span>
        <h1 id="product-title">{t('auth.headline_1')}<br /><span>{t('auth.headline_2')}</span></h1>
        <p>{t('auth.intro')}</p>
        <div className="login-features">
          <div><ChartNoAxesCombined size={20} /><span><strong>{t('auth.feature_visibility')}</strong><small>{t('auth.feature_visibility_desc')}</small></span></div>
          <div><Layers size={20} /><span><strong>{t('auth.feature_operations')}</strong><small>{t('auth.feature_operations_desc')}</small></span></div>
          <div><ShieldCheck size={20} /><span><strong>{t('auth.feature_access')}</strong><small>{t('auth.feature_access_desc')}</small></span></div>
        </div>
        <span className="login-story-footer">{t('auth.story_footer')}</span>
      </section>
      <section className="login-card" aria-labelledby="login-title">
        <div className="login-header"><span className="login-badge"><LockKeyhole size={23} /></span>
          <h2 id="login-title" className="login-title">{t('auth.welcome_back')}</h2><p className="login-subtitle">{t('auth.subtitle')}</p>
        </div>
        {error && <div className="error-banner" role="alert"><AlertCircle size={18} /><span>{error}</span></div>}
        <form onSubmit={handleSubmit} className="login-form">
          <div className="form-group"><label htmlFor="username">{t('auth.username')}</label><div className="input-with-icon">
            <User size={17} className="input-icon" /><input id="username" type="text" autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} placeholder={t('auth.username_placeholder')} required disabled={loading} />
          </div></div>
          <div className="form-group"><label htmlFor="password">{t('auth.password')}</label><div className="input-with-icon">
            <LockKeyhole size={17} className="input-icon" /><input id="password" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} placeholder={t('auth.password_placeholder')} required disabled={loading} />
          </div></div>
          <button type="submit" className="btn btn-primary btn-block" disabled={loading} aria-busy={loading}>
            {loading ? <><span className="spinner-inline" />{t('auth.signing_in')}</> : <>{t('auth.sign_in')}<ArrowRight size={17} /></>}
          </button>
        </form>
        <div className="login-footer"><ShieldCheck size={15} /><span>{t('auth.authorized_only')}<br />{t('auth.contact_admin')}</span></div>
      </section>
    </div>
  </div>
}
