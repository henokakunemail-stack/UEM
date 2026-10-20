import React from 'react'
import { useT } from '../i18n/core'

interface ErrorBoundaryProps {
  children: React.ReactNode
  /** Shown as context for the error, e.g. "Maintenance". */
  label?: string
}

interface ErrorBoundaryState {
  error: Error | null
}

/** The fallback is a function component so it can read the current language;
 *  the boundary itself has to stay a class. */
function ErrorFallback({ label, message, onRetry }: { label?: string; message: string; onRetry: () => void }) {
  const { t } = useT()
  return (
    <div className="empty-state" role="alert">
      <h1>{t('ui.page_failed', { page: label ?? t('ui.this_page') })}</h1>
      <p>{message}</p>
      <button type="button" className="btn btn-secondary" onClick={onRetry}>
        {t('ui.try_again')}
      </button>
      <button
        type="button"
        className="btn btn-secondary"
        onClick={() => {
          window.location.href = '/dashboard'
        }}
      >
        {t('ui.return_to_dashboard')}
      </button>
    </div>
  )
}

/**
 * A render throw inside a page unmounts the whole React tree, so the console
 * goes to a blank white screen with nothing in the console to point at. This
 * catches it at the page boundary instead: the shell and sidebar stay usable
 * and the failure is reported in place.
 *
 * Class component because componentDidCatch has no hook equivalent.
 */
export class ErrorBoundary extends React.Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Nothing here can be reported to a server; the browser console is the
    // only place this lands, so make it carry the component stack too.
    console.error(`[${this.props.label ?? 'page'}] render failed`, error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <ErrorFallback
        label={this.props.label}
        message={this.state.error.message}
        onRetry={() => this.setState({ error: null })}
      />
    )
  }
}
