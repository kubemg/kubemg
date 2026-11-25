/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { BrandingContext } from '../state/branding-context'
import { AppFooter } from './AppFooter'
import { FeedbackSheet } from './FeedbackSheet'

vi.mock('../api/client', () => ({
  fetchServerVersion: () => Promise.resolve({ version: 'v0.17.2', docs_url: 'https://kubemg.readthedocs.io/' }),
}))

/*
 * The feedback button hands a note to GitHub Discussions and sends nothing
 * itself: what matters is the address it opens, because that is the whole of
 * what reaches GitHub.
 */

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function fill(title: string, message: string) {
  fireEvent.change(screen.getByLabelText('Title'), { target: { value: title } })
  fireEvent.change(screen.getByLabelText('Message'), { target: { value: message } })
}

function continueOnGitHub() {
  return screen.getByRole<HTMLButtonElement>('button', { name: /Continue on GitHub/ })
}

describe('FeedbackSheet', () => {
  it('waits for a title and a message', () => {
    render(<FeedbackSheet version="v0.17.2" onClose={() => {}} />)
    expect(continueOnGitHub().disabled).toBe(true)

    fill('A title', '   ')
    expect(continueOnGitHub().disabled).toBe(true)

    fill('A title', 'A message')
    expect(continueOnGitHub().disabled).toBe(false)
  })

  it('opens a new discussion with the note and the release filled in', () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const onClose = vi.fn()
    render(<FeedbackSheet version="v0.17.2" onClose={onClose} />)

    fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'q-a' } })
    fill('  Can the audit page export JSON?  ', 'CSV is there; JSON would\nsuit our SIEM.')
    fireEvent.click(continueOnGitHub())

    expect(open).toHaveBeenCalledTimes(1)
    const [address, target, features] = open.mock.calls[0]
    expect(target).toBe('_blank')
    expect(features).toContain('noopener')
    const url = new URL(String(address))
    expect(url.origin + url.pathname).toBe('https://github.com/kubemg/kubemg/discussions/new')
    expect(url.searchParams.get('category')).toBe('q-a')
    expect(url.searchParams.get('title')).toBe('Can the audit page export JSON?')
    expect(url.searchParams.get('body')).toBe('CSV is there; JSON would\nsuit our SIEM.\n\n---\nkubemg v0.17.2')
    expect(onClose).toHaveBeenCalled()
  })

  it('offers ideas first', () => {
    render(<FeedbackSheet onClose={() => {}} />)
    expect(screen.getByLabelText<HTMLSelectElement>('Kind').value).toBe('ideas')
  })

  it('measures the budget on the encoded address, not on the characters typed', () => {
    render(<FeedbackSheet version="v0.17.2" onClose={() => {}} />)

    // 1500 ASCII characters encode to about as many: well inside the address.
    fill('Ideas', 'a'.repeat(1500))
    expect(continueOnGitHub().disabled).toBe(false)

    // 1500 Turkish ones encode to six characters each, past what GitHub takes,
    // though the text is no longer.
    fill('Fikirler', 'ş'.repeat(1500))
    expect(continueOnGitHub().disabled).toBe(true)
    expect(screen.getByRole('alert').textContent).toMatch(/Too long for one address/)
  })

  it('keeps the title short', () => {
    render(<FeedbackSheet onClose={() => {}} />)
    expect(screen.getByLabelText<HTMLInputElement>('Title').maxLength).toBe(200)
  })
})

describe('AppFooter feedback', () => {
  it('is not drawn when an administrator turned it off', async () => {
    render(
      <BrandingContext.Provider value={{ branding: { feedback_hidden: true }, refresh: () => Promise.resolve() }}>
        <AppFooter />
      </BrandingContext.Provider>,
    )
    expect(await screen.findByRole('link', { name: /Documentation/ })).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Feedback' })).toBeNull()
  })

  it('sits beside Documentation and opens the sheet', async () => {
    render(
      <BrandingContext.Provider value={{ branding: null, refresh: () => Promise.resolve() }}>
        <AppFooter />
      </BrandingContext.Provider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Feedback' }))

    expect(screen.getByRole('link', { name: /Documentation/ })).not.toBeNull()
    expect(screen.getByRole('dialog', { name: 'Send feedback' })).not.toBeNull()
  })
})
