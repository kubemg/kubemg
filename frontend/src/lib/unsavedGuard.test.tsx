/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Link, MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import { ConfirmProvider } from '../state/ConfirmProvider'
import { useUnsavedGuard } from './unsavedGuard'

afterEach(cleanup)

function Editor({ dirty }: { dirty: boolean }) {
  useUnsavedGuard(dirty, 'these settings')
  return <Link to="/elsewhere">Go elsewhere</Link>
}

function app(dirty: boolean) {
  render(
    <MemoryRouter initialEntries={['/settings']}>
      <ConfirmProvider>
        <Routes>
          <Route path="/settings" element={<Editor dirty={dirty} />} />
          <Route path="/elsewhere" element={<p>Somewhere else</p>} />
        </Routes>
      </ConfirmProvider>
    </MemoryRouter>,
  )
}

describe('leaving a page with an unsaved edit', () => {
  it('asks first, and stays when the answer is no', async () => {
    app(true)
    fireEvent.click(screen.getByText('Go elsewhere'))
    await waitFor(() => expect(screen.getByText('Leave without saving?')).toBeTruthy())
    expect(screen.queryByText('Somewhere else')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByText('Leave without saving?')).toBeNull())
    expect(screen.getByText('Go elsewhere')).toBeTruthy()
  })

  it('goes where the link pointed when the answer is to leave', async () => {
    app(true)
    fireEvent.click(screen.getByText('Go elsewhere'))
    fireEvent.click(await screen.findByRole('button', { name: 'Leave' }))
    await waitFor(() => expect(screen.getByText('Somewhere else')).toBeTruthy())
  })

  it('does not ask when nothing has changed', () => {
    app(false)
    fireEvent.click(screen.getByText('Go elsewhere'))
    expect(screen.queryByText('Leave without saving?')).toBeNull()
    expect(screen.getByText('Somewhere else')).toBeTruthy()
  })
})
