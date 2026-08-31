/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { MachineAccount, MachineAccountAccess } from '../api/types'
import { SetupPath } from './MachineAccounts'

/*
 * The three surfaces this page offers have to be used in one order — account,
 * grant, credential — and nothing on the page said so: the grant lives one
 * click inside the account's own name, and a credential issued before it is
 * refused by the server. The strip is the only place that order is written
 * down, so it is asserted rather than left to survive a refactor by luck.
 */

const grant: MachineAccountAccess = {
  cluster_id: 1,
  cluster_name: 'prod',
  k8s_role: 'edit',
  namespaces: [],
}

function account(over: Partial<MachineAccount>): MachineAccount {
  return {
    id: 1,
    username: 'jenkins-release',
    role: 'user',
    is_active: true,
    created_at: '2026-08-01T00:00:00Z',
    token_count: 0,
    active_tokens: 0,
    access: [],
    ...over,
  } as MachineAccount
}

afterEach(cleanup)

describe('the order a machine account is set up in', () => {
  it('names all three steps, in order', () => {
    render(<SetupPath accounts={[]} />)
    const steps = screen.getAllByRole('listitem').map((item) => item.textContent ?? '')

    expect(steps).toHaveLength(3)
    expect(steps[0]).toContain('Add the account')
    expect(steps[1]).toContain('Grant it a cluster')
    expect(steps[2]).toContain('Issue a credential')
    // The step nobody finds on their own: the grant is inside the account's own
    // name, and a credential issued before it exists is refused by the cluster.
    expect(steps[1]).toContain('Open its name')
    expect(steps[1]).toMatch(/refused/)
  })

  it('numbers a step nobody has taken yet', () => {
    const { container } = render(<SetupPath accounts={[]} />)
    expect(container.textContent).toContain('1')
    expect(container.textContent).toContain('2')
    expect(container.textContent).toContain('3')
  })

  it('stops numbering a step this installation has already taken', () => {
    // Read off the fleet rather than a counter: an account with a grant and a
    // live credential means all three steps are behind this operator, and the
    // strip has to read as that rather than as a tutorial that never finishes.
    const { container } = render(
      <SetupPath accounts={[account({ access: [grant], token_count: 1, active_tokens: 1 })]} />,
    )
    expect(container.querySelectorAll('ol > li')).toHaveLength(3)
    expect(container.textContent).not.toMatch(/[123]/)
  })

  it('leaves the later steps numbered while only the account exists', () => {
    const { container } = render(<SetupPath accounts={[account({})]} />)
    expect(container.textContent).not.toContain('1')
    expect(container.textContent).toContain('2')
    expect(container.textContent).toContain('3')
  })
})
