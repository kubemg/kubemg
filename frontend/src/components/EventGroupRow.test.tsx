/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { EventGroup } from '../api/types'
import { EventGroupRow } from './EventGroupRow'

/*
 * The row is shared by the cluster timeline and a namespace's page, which is
 * the reason it is worth a test at all: a change that reads well on one of them
 * now changes both, and the two must not start describing the cluster's own
 * words differently.
 */

const group: EventGroup = {
  key: 'Pod/shop/web-1',
  warnings: 40,
  object: { kind: 'Pod', name: 'web-1', namespace: 'shop' },
  type: 'Warning',
  reason: 'BackOff',
  message: 'Back-off restarting failed container',
  count: 41,
  last_seen: '2026-08-27T09:00:00Z',
  entries: [
    {
      type: 'Warning',
      reason: 'BackOff',
      message: 'Back-off restarting failed container',
      count: 40,
      first_seen: '2026-08-27T08:00:00Z',
      last_seen: '2026-08-27T09:00:00Z',
      source: 'kubelet',
    },
    {
      type: 'Normal',
      reason: 'Pulled',
      message: 'Container image already present',
      count: 1,
      last_seen: '2026-08-27T08:00:00Z',
    },
  ],
}

afterEach(cleanup)

describe('EventGroupRow', () => {
  it('collapsed, is the newest reason and how often it fired', () => {
    render(<EventGroupRow group={group} open={false} onToggle={() => {}} />)

    expect(screen.getByText('BackOff')).toBeTruthy()
    expect(screen.getByText('Back-off restarting failed container')).toBeTruthy()
    // A count of firings rather than of rows: 41 is what tells a flake from a
    // loop, and folding it away would lose the only number on the line.
    expect(screen.getByText('×41')).toBeTruthy()
    expect(screen.getByRole('button', { expanded: false })).toBeTruthy()
    // The reasons underneath are not drawn until it is opened.
    expect(screen.queryByText('Pulled')).toBeNull()
  })

  it('opened, is every reason the cluster folded into it', () => {
    render(<EventGroupRow group={group} open onToggle={() => {}} />)

    expect(screen.getByText('Pulled')).toBeTruthy()
    expect(screen.getByText('Container image already present')).toBeTruthy()
    expect(screen.getByRole('button', { expanded: true })).toBeTruthy()
  })
})
