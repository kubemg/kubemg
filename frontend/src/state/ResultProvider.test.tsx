/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import { ResultProvider } from './ResultProvider'
import { useResult } from './result-context'
import type { ResultReport } from './result-context'

/*
 * What the strip has to get right is which results go away on their own. A
 * success is a courtesy and takes itself off; a failure is the one sentence
 * nobody may miss, and it waits — the reader very likely looked away precisely
 * because they assumed the act had landed.
 */

afterEach(cleanup)

function Harness({ result }: { result: ResultReport }) {
  const report = useResult()
  return (
    <button type="button" onClick={() => report(result)}>
      Do it
    </button>
  )
}

function mount(result: ResultReport) {
  render(
    <MemoryRouter>
      <ResultProvider>
        <Harness result={result} />
      </ResultProvider>
    </MemoryRouter>,
  )
  fireEvent.click(screen.getByRole('button', { name: 'Do it' }))
}

describe('saying what just happened', () => {
  it('reports the act in the control’s own word, with the record it produced', () => {
    mount({
      tone: 'ok',
      title: 'Uninstalled',
      body: 'Objects the chart rendered are gone.',
      link: { to: '/audit', label: 'See it in the audit trail' },
    })

    expect(screen.getByText('Uninstalled')).toBeTruthy()
    expect(screen.getByText('Objects the chart rendered are gone.')).toBeTruthy()
    const link = screen.getByRole('link', { name: 'See it in the audit trail' })
    expect(link.getAttribute('href')).toBe('/audit')
  })

  it('leaves a failure up until it is dismissed', async () => {
    mount({ tone: 'error', title: 'Nothing was revoked', body: 'the cluster could not be reached' })

    expect(screen.getByText('Nothing was revoked')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByText('Nothing was revoked')).toBeNull())
  })

  it('is announced politely rather than interrupting what is being read', () => {
    mount({ tone: 'ok', title: 'Deleted alice' })
    const live = document.querySelector('[aria-live]')
    expect(live?.getAttribute('aria-live')).toBe('polite')
  })
})
