import React, { useState } from 'react'
import { Upload } from 'lucide-react'
import { api } from '../services/api'
import { apiErrorMessage } from '../services/apiErrorMessage'
import { MAX_BULK_PATTERNS, parseDomainLines } from '../services/domainImport'
import { Modal } from './ui/Modal'
import { useT } from '../i18n/core'
import type { BulkRuleResponseDTO, FilterPolicyDTO } from '../types/api'

// Reason codes the server returns for a rejected line, mapped to their text.
export const FILTER_REASON_KEYS: Record<string, string> = {
  empty: 'filter.reason_empty',
  invalid_chars: 'filter.reason_invalid_chars',
  ip_literal: 'filter.reason_ip_literal',
  too_long: 'filter.reason_too_long',
  no_tld: 'filter.reason_no_tld',
  idn_not_supported: 'filter.reason_idn_not_supported',
  duplicate_existing: 'filter.reason_duplicate_existing',
  duplicate_in_batch: 'filter.reason_duplicate_in_batch',
  too_many_patterns: 'filter.reason_too_many_patterns',
}

interface Props {
  policy: FilterPolicyDTO | null
  categories: { value: string; label: string }[]
  onClose: () => void
  /** Called after a successful import so the page can reload its lists. */
  onImported: (summary: BulkRuleResponseDTO) => void
}

/**
 * Adds many domains to one policy in a single request. Domains used to go in one
 * at a time, each re-sending the whole policy to every affected device; here the
 * operator pastes or imports a list, the server checks every line, and the
 * report says exactly which lines were added, already present, or rejected.
 */
export const FilterBulkAddModal: React.FC<Props> = ({ policy, categories, onClose, onImported }) => {
  const { t } = useT()
  const [text, setText] = useState('')
  const [category, setCategory] = useState(categories[0]?.value ?? '')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<BulkRuleResponseDTO | null>(null)
  const [fileName, setFileName] = useState('')

  const patterns = parseDomainLines(text, fileName)
  const whitelist = policy?.mode === 'whitelist'

  const close = () => {
    setText('')
    setFileName('')
    setResult(null)
    setError('')
    onClose()
  }

  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    const content = await file.text()
    setFileName(file.name)
    setText(parseDomainLines(content, file.name).join('\n'))
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!policy || patterns.length === 0) return
    setSubmitting(true)
    setError('')
    try {
      const summary = await api.bulkAddFilterRules(policy.id, { patterns, category })
      setResult(summary)
      onImported(summary)
    } catch (err) {
      setError(apiErrorMessage(err, t, FILTER_REASON_KEYS, 'filter.bulk_failed'))
    } finally {
      setSubmitting(false)
    }
  }

  const rejected = result ? result.results.filter((r) => r.status !== 'added') : []

  return (
    <Modal
      open={policy !== null}
      onClose={close}
      title={whitelist ? t('filter.bulk_title_allowed') : t('filter.bulk_title_blocked')}
      size="lg"
      dismissible={!submitting}
      description={policy ? t('filter.bulk_description', { name: policy.name, max: MAX_BULK_PATTERNS }) : undefined}
      footer={
        result ? (
          <button type="button" className="btn btn-primary" onClick={close}>
            {t('filter.bulk_done')}
          </button>
        ) : (
          <>
            <button type="button" className="btn btn-secondary" onClick={close} disabled={submitting}>
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="filter-bulk-form"
              className="btn btn-primary"
              disabled={submitting || patterns.length === 0 || patterns.length > MAX_BULK_PATTERNS}
            >
              {t('filter.bulk_submit', { count: patterns.length })}
            </button>
          </>
        )
      }
    >
      {result ? (
        <div>
          <p className="text-main" role="status">
            {t('filter.bulk_summary', { added: result.added, duplicates: result.duplicates, invalid: result.invalid })}
          </p>
          {rejected.length > 0 && (
            <table className="data-table" aria-label={t('filter.bulk_rejected_label')}>
              <thead>
                <tr>
                  <th>{t('filter.bulk_col_line')}</th>
                  <th>{t('filter.bulk_col_result')}</th>
                </tr>
              </thead>
              <tbody>
                {rejected.map((r, i) => (
                  <tr key={`${r.input}-${i}`}>
                    <td className="font-mono">{r.input}</td>
                    <td>{t(FILTER_REASON_KEYS[r.reason ?? ''] ?? 'filter.reason_unknown')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      ) : (
        <form id="filter-bulk-form" onSubmit={submit}>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-bulk-text">
              {t('filter.bulk_label')}
            </label>
            <textarea
              id="filter-bulk-text"
              className="form-input font-mono"
              rows={10}
              placeholder={t('filter.bulk_placeholder')}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
            <p className="form-hint">
              {t('filter.bulk_count', { count: patterns.length })}
              {' '}
              {whitelist ? t('filter.subdomain_tip') : t('filter.blacklist_subdomain_tip')}
            </p>
          </div>
          <div className="form-group">
            <label className="btn btn-secondary" htmlFor="filter-bulk-file">
              <Upload size={16} />
              <span>{t('filter.bulk_import')}</span>
            </label>
            <input
              id="filter-bulk-file"
              type="file"
              accept=".txt,.csv,text/plain,text/csv"
              className="visually-hidden"
              onChange={onFile}
            />
            {fileName && <span className="text-sm text-dim"> {fileName}</span>}
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-bulk-category">
              {t('filter.col_category')}
            </label>
            <select
              id="filter-bulk-category"
              className="form-select"
              value={category}
              onChange={(e) => setCategory(e.target.value)}
            >
              {categories.map((c) => (
                <option key={c.value} value={c.value}>
                  {c.label}
                </option>
              ))}
            </select>
          </div>
          {error && (
            <p className="text-danger" role="alert">
              {error}
            </p>
          )}
        </form>
      )}
    </Modal>
  )
}
