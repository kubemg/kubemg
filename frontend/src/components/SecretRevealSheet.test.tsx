/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Cluster, ConfigEntry, SecretValue } from '../api/types'
import { SecretRevealSheet } from './SecretRevealSheet'

/*
 * The one surface in this console that shows a credential. What is asserted
 * here is the discipline around it rather than the layout: opening it reads
 * nothing, each key is its own request, and a value that is not text is not
 * quietly mangled into one.
 */

/*
 * The client is stood in for by hand rather than by `vi.fn`: one of these tests
 * needs the call to fail, and a mock function that returns a rejected promise
 * leaves vitest holding one nobody awaited. `calls` records what was asked for,
 * which is the half the assertions actually need.
 */
const calls: unknown[][] = []
let answer: () => Promise<SecretValue> = async () => value()

vi.mock('../api/client', () => ({
  revealSecretValue: (...args: unknown[]) => {
    calls.push(args)
    return answer()
  },
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

const cluster = { id: 7, name: 'prod' } as Cluster

const entry: ConfigEntry = {
  name: 'db-credentials',
  namespace: 'shop',
  created_at: '2026-01-01T00:00:00Z',
  type: 'Opaque',
  keys: ['username', 'password'],
}

function value(over: Partial<SecretValue> = {}): SecretValue {
  return {
    namespace: 'shop',
    name: 'db-credentials',
    key: 'password',
    value: 'hunter2',
    binary: false,
    bytes: 7,
    ...over,
  }
}

beforeEach(() => {
  calls.length = 0
  answer = async () => value()
})
afterEach(cleanup)

describe('SecretRevealSheet', () => {
  it('reads nothing until a key is asked for', () => {
    render(<SecretRevealSheet cluster={cluster} entry={entry} onClose={() => {}} />)

    // The sheet lists the keys the list already carried. A sheet that fetched
    // on open would turn browsing to the right Secret into a reveal of every
    // value in the wrong ones.
    expect(calls).toHaveLength(0)
    expect(screen.getAllByRole('button', { name: /^Reveal / })).toHaveLength(2)
    expect(screen.queryByText('hunter2')).toBeNull()
  })

  it('asks for one key at a time and shows only that one', async () => {
    render(<SecretRevealSheet cluster={cluster} entry={entry} onClose={() => {}} />)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Reveal password' }))
    })

    await waitFor(() => expect(screen.getByText('hunter2')).toBeTruthy())
    expect(calls).toEqual([[7, 'shop', 'db-credentials', 'password']])
    // The other key is still a button, not a value: two reveals are two
    // requests and two lines in the audit trail, which is the whole point of
    // addressing one key at a time.
    expect(screen.getByRole('button', { name: 'Reveal username' })).toBeTruthy()
  })

  it('leaves a value that is not text encoded rather than mangling it', async () => {
    answer = async () =>
      value({ key: 'tls.key', value: undefined, binary: true, encoded: 'MIIB//4=', bytes: 4 })
    const tls: ConfigEntry = { ...entry, name: 'tls', keys: ['tls.key'] }
    render(<SecretRevealSheet cluster={cluster} entry={tls} onClose={() => {}} />)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Reveal tls.key' }))
    })

    // "Is this the right certificate" cannot be answered from a decode that
    // replaced half the bytes, so the console says it is binary and shows what
    // the cluster actually stores.
    await waitFor(() => expect(screen.getByText(/not text/)).toBeTruthy())
    expect(screen.getByText('MIIB//4=')).toBeTruthy()
  })

  it("hands back the server's refusal against the key it was asked for", async () => {
    answer = async () => {
      throw new Error('nope')
    }
    render(<SecretRevealSheet cluster={cluster} entry={entry} onClose={() => {}} />)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Reveal password' }))
    })

    await waitFor(() => expect(screen.getByText('password could not be read.')).toBeTruthy())
    // A refusal is not a reveal: the other key keeps its own control, and
    // nothing about this one has been shown.
    expect(screen.getByRole('button', { name: 'Reveal username' })).toBeTruthy()
  })

  it('says what a reveal costs before anything is clicked', () => {
    render(<SecretRevealSheet cluster={cluster} entry={entry} onClose={() => {}} />)

    // The accounting is the reason this surface exists rather than a jsonpath
    // in a terminal, so it is stated up front rather than discovered in the
    // trail afterwards.
    expect(screen.getByText(/audit trail/)).toBeTruthy()
  })
})
