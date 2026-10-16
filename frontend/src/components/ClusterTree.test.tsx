/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import type { Cluster } from '../api/types'
import { ClusterTree } from './ClusterTree'

/*
 * The Cluster group keeps the everyday pages; the reports fold into Insights,
 * which starts closed and opens only for the page on screen or a click.
 */

const cluster = { id: 3, name: 'prod', connection_mode: 'direct' } as unknown as Cluster

function renderTree(pathname: string) {
  return render(
    <MemoryRouter initialEntries={[pathname]}>
      <ClusterTree cluster={cluster} categories={[]} selected={null} />
    </MemoryRouter>,
  )
}

afterEach(cleanup)

describe('ClusterTree', () => {
  it('folds the reports under a closed Insights section', () => {
    renderTree('/clusters/3/dashboard')
    expect(screen.getByText('Dashboard')).toBeTruthy()
    expect(screen.getByText('Alerts')).toBeTruthy()
    const insights = screen.getByRole('button', { name: 'Insights' })
    expect(insights.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('Cost & waste')).toBeNull()

    fireEvent.click(insights)
    expect(screen.getByText('Cost & waste')).toBeTruthy()
  })

  it('opens Insights when one of its pages is on screen', () => {
    renderTree('/clusters/3/audit')
    expect(screen.getByRole('button', { name: 'Insights' }).getAttribute('aria-expanded')).toBe(
      'true',
    )
    expect(screen.getByText('Audit trail')).toBeTruthy()
  })
})
