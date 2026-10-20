import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { dictionaries, translate, useT } from './core'
import { I18nProvider } from './I18nContext'
import { LanguageToggle } from './LanguageToggle'

describe('translate', () => {
  it('fills placeholders and leaves unknown ones visible', () => {
    dictionaries.en['test.greet'] = 'Hello {name}, {missing}'
    expect(translate('en', 'test.greet', { name: 'Henok' })).toBe('Hello Henok, {missing}')
    delete dictionaries.en['test.greet']
  })

  it('falls back to English, then to the key, never to an empty string', () => {
    dictionaries.en['test.only_en'] = 'English only'
    expect(translate('id', 'test.only_en')).toBe('English only')
    expect(translate('id', 'test.nowhere')).toBe('test.nowhere')
    delete dictionaries.en['test.only_en']
  })

  it('has the same keys in every language', () => {
    expect(Object.keys(dictionaries.id).sort()).toEqual(Object.keys(dictionaries.en).sort())
  })
})

function Probe() {
  const { t, lang } = useT()
  return <p data-testid="probe">{lang}:{t('common.cancel')}</p>
}

describe('LanguageToggle', () => {
  it('switches the language, remembers it, and sets <html lang>', () => {
    render(
      <I18nProvider lang="en">
        <LanguageToggle />
        <Probe />
      </I18nProvider>,
    )
    expect(screen.getByTestId('probe').textContent).toBe('en:Cancel')

    fireEvent.click(screen.getByRole('button', { name: 'ID' }))
    expect(screen.getByTestId('probe').textContent).toBe('id:Batal')
    expect(localStorage.getItem('em_lang')).toBe('id')
    expect(document.documentElement.lang).toBe('id')
    expect(screen.getByRole('button', { name: 'ID' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('works without a provider, in English', () => {
    render(<Probe />)
    expect(screen.getByTestId('probe').textContent).toBe('en:Cancel')
  })
})
