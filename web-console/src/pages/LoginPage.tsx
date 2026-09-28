import { useState, type FormEvent } from 'react'
import { AlertCircle, ArrowRight, ChartNoAxesCombined, Layers, LockKeyhole, Monitor, ShieldCheck, User } from 'lucide-react'
import { useAuth } from '../context/AuthContext'
import { ThemeToggle } from '../context/ThemeContext'

export function LoginPage() {
  const { login } = useAuth()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setError(null)
    setLoading(true)
    try { await login(username, password) }
    catch (error) { setError(error instanceof Error ? error.message : 'Unable to sign in') }
    finally { setLoading(false) }
  }

  return <div className="login-screen">
    <div className="login-theme"><ThemeToggle /></div>
    <div className="login-layout">
      <section className="login-story" aria-labelledby="product-title">
        <div className="login-wordmark"><Monitor size={23} /><span>Enterprise Endpoint Manager</span></div>
        <span className="eyebrow">YOUR FLEET. ONE WORKSPACE.</span>
        <h1 id="product-title">A clear view.<br /><span>Complete control.</span></h1>
        <p>Keep devices healthy, operations moving, and every endpoint accounted for.</p>
        <div className="login-features">
          <div><ChartNoAxesCombined size={20} /><span><strong>Fleet visibility</strong><small>Hardware, health, and activity in one place.</small></span></div>
          <div><Layers size={20} /><span><strong>Connected operations</strong><small>Deploy software, schedule tasks, manage updates.</small></span></div>
          <div><ShieldCheck size={20} /><span><strong>Controlled access</strong><small>Permissions built around your team's roles.</small></span></div>
        </div>
        <span className="login-story-footer">MONITOR · MANAGE · MAINTAIN</span>
      </section>
      <section className="login-card" aria-labelledby="login-title">
        <div className="login-header"><span className="login-badge"><LockKeyhole size={23} /></span>
          <h2 id="login-title" className="login-title">Welcome back</h2><p className="login-subtitle">Sign in to your operations workspace.</p>
        </div>
        {error && <div className="error-banner" role="alert"><AlertCircle size={18} /><span>{error}</span></div>}
        <form onSubmit={handleSubmit} className="login-form">
          <div className="form-group"><label htmlFor="username">Username</label><div className="input-with-icon">
            <User size={17} className="input-icon" /><input id="username" type="text" autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} placeholder="Enter your username" required disabled={loading} />
          </div></div>
          <div className="form-group"><label htmlFor="password">Password</label><div className="input-with-icon">
            <LockKeyhole size={17} className="input-icon" /><input id="password" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} placeholder="Enter your password" required disabled={loading} />
          </div></div>
          <button type="submit" className="btn btn-primary btn-block" disabled={loading} aria-busy={loading}>
            {loading ? <><span className="spinner-inline" />Signing in…</> : <>Sign in<ArrowRight size={17} /></>}
          </button>
        </form>
        <div className="login-footer"><ShieldCheck size={15} /><span>Authorized personnel only.<br />Contact your administrator for access.</span></div>
      </section>
    </div>
  </div>
}
