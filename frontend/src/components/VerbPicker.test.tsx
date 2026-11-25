/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { VerbPicker } from './VerbPicker'
import type { VerbGroup } from './VerbPicker'

/*
 * The audit trail's verb filter as one control (#369). What matters is that it
 * hands the page the same set the chips did, that it says what is selected
 * while closed, and that a keyboard can work it.
 */

afterEach(cleanup)

const GROUPS: VerbGroup[] = [
  { label: 'Reads', verbs: ['get', 'list'] },
  { label: 'Writes', verbs: ['create', 'delete'] },
]

function Harness({ onChange }: { onChange?: (next: string[]) => void }) {
  const [selected, setSelected] = useState<string[]>([])
  return (
    <VerbPicker
      groups={GROUPS}
      selected={selected}
      onChange={(next) => {
        onChange?.(next)
        setSelected(next)
      }}
    />
  )
}

function trigger() {
  return screen.getByRole('button', { name: /Filter by verb/ })
}

describe('VerbPicker', () => {
  it('says what is selected while closed', () => {
    render(<Harness />)
    expect(trigger().textContent).toContain('All verbs')

    fireEvent.click(trigger())
    fireEvent.click(screen.getByRole('checkbox', { name: 'delete' }))
    expect(trigger().textContent).toContain('delete')

    fireEvent.click(screen.getByRole('checkbox', { name: 'get' }))
    expect(trigger().textContent).toContain('2 verbs')
  })

  it('hands the page the set, and clears it', () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)
    fireEvent.click(trigger())

    fireEvent.click(screen.getByRole('checkbox', { name: 'create' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'list' }))
    expect(onChange).toHaveBeenLastCalledWith(['create', 'list'])

    fireEvent.click(screen.getByRole('checkbox', { name: 'create' }))
    expect(onChange).toHaveBeenLastCalledWith(['list'])

    fireEvent.click(screen.getByRole('button', { name: /Clear/ }))
    expect(onChange).toHaveBeenLastCalledWith([])
  })

  it('groups the verbs and finds them by name', () => {
    render(<Harness />)
    fireEvent.click(trigger())
    expect(screen.getByRole('group', { name: 'Reads' })).not.toBeNull()
    expect(screen.getByRole('group', { name: 'Writes' })).not.toBeNull()

    fireEvent.change(screen.getByRole('searchbox', { name: 'Find a verb' }), { target: { value: 'del' } })
    expect(screen.getAllByRole('checkbox').map((box) => box.getAttribute('aria-label') ?? box.parentElement?.textContent)).toEqual(['delete'])
    expect(screen.queryByRole('group', { name: 'Reads' })).toBeNull()
  })

  it('closes on Escape and hands focus back to its button', () => {
    render(<Harness />)
    fireEvent.click(trigger())
    expect(trigger().getAttribute('aria-expanded')).toBe('true')

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog', { name: 'Verbs' })).toBeNull()
    expect(trigger().getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger())
  })
})
