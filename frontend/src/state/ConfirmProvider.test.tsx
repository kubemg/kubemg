/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { Sheet } from '../components/primitives'
import { ConfirmProvider } from './ConfirmProvider'
import { useConfirm } from './confirm-context'

/*
 * The contract a caller depends on: the promise settles, exactly once, on every
 * way out of the sheet. Fourteen destructive acts sit behind an `await` on it,
 * and one that never settles is an act that silently never happens — the one
 * failure mode a confirmation may not have.
 */

afterEach(cleanup)

function Harness({ onAnswer }: { onAnswer: (answer: boolean) => void }) {
  const confirm = useConfirm()
  return (
    <button
      type="button"
      onClick={() => {
        void confirm({
          title: 'Delete alice?',
          body: 'Their grants and group memberships go with them.',
          confirmLabel: 'Delete',
        }).then(onAnswer)
      }}
    >
      Delete account
    </button>
  )
}

function ask(onAnswer: (answer: boolean) => void) {
  render(
    <ConfirmProvider>
      <Harness onAnswer={onAnswer} />
    </ConfirmProvider>,
  )
}

describe('asking before something irreversible', () => {
  it('says what is going and answers the caller yes', async () => {
    const answers: boolean[] = []
    ask((answer) => answers.push(answer))

    fireEvent.click(screen.getByRole('button', { name: 'Delete account' }))
    // The act, its consequence and the control's own word — none of which the
    // operating system's box could carry.
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(screen.getByText('Delete alice?')).toBeTruthy()
    expect(screen.getByText(/group memberships go with them/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(answers).toEqual([true]))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('answers no on cancel', async () => {
    const answers: boolean[] = []
    ask((answer) => answers.push(answer))

    fireEvent.click(screen.getByRole('button', { name: 'Delete account' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(answers).toEqual([false]))
  })

  it('answers no when the sheet is closed rather than answered', async () => {
    // Escape and the scrim both reach `Sheet`'s onClose, and a caller waiting on
    // the promise must not be left holding it.
    const answers: boolean[] = []
    ask((answer) => answers.push(answer))

    fireEvent.click(screen.getByRole('button', { name: 'Delete account' }))
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(answers).toEqual([false]))
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('a confirmation over another sheet', () => {
  it('takes Escape from the sheet underneath rather than sharing it', async () => {
    // The detail drawer's own Escape is what asks "discard your changes?", so a
    // shared listener re-asked the question on every press instead of answering
    // it. Only the topmost sheet answers.
    const closes: string[] = []
    function Underneath() {
      const confirm = useConfirm()
      return (
        <Sheet title="Edit values" onClose={() => void confirm({
          title: 'Discard your unsaved changes?',
          body: 'Closing throws away what you typed.',
          confirmLabel: 'Discard',
        }).then((answer) => { if (answer) closes.push('closed') })}>
          <p>values</p>
        </Sheet>
      )
    }

    render(
      <ConfirmProvider>
        <Underneath />
      </ConfirmProvider>,
    )

    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.getByText('Discard your unsaved changes?')).toBeTruthy())

    // The second Escape answers the question. It must not reach the sheet below
    // and ask it again.
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByText('Discard your unsaved changes?')).toBeNull())
    expect(closes).toEqual([])
  })
})
