/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { Cluster, DataEntries } from '../api/types'
import { ConfigDataView } from './ConfigDataPanel'

const cluster = { id: 4, name: 'e2e-minikube' } as Cluster

const configMap: DataEntries = {
  kind: 'ConfigMap',
  immutable: false,
  values_shown: true,
  entries: [
    { key: 'app.yaml', value: 'workers: 2\n', bytes: 11, binary: false },
    { key: 'logo.png', bytes: 8, binary: true },
  ],
}

const secret: DataEntries = {
  kind: 'Secret',
  type: 'Opaque',
  immutable: false,
  values_shown: false,
  entries: [{ key: 'password', bytes: 7, binary: false }],
}

function draw(data: DataEntries, canReveal: boolean) {
  return render(<ConfigDataView cluster={cluster} name="shop" namespace="e2e-apps" data={data} canReveal={canReveal} />)
}

afterEach(cleanup)

describe('what a ConfigMap or Secret holds', () => {
  it('draws a ConfigMap value by its key, and binary data as a size only', () => {
    const { container } = draw(configMap, false)
    expect(container.textContent).toContain('app.yaml')
    expect(container.textContent).toContain('workers: 2')
    expect(container.textContent).toContain('Binary data — not shown as text.')
  })

  it('offers a Secret key to reveal only to a caller who holds the capability', () => {
    draw(secret, true)
    expect(screen.getByRole('button', { name: 'Reveal password' })).toBeTruthy()
  })

  it('tells anybody else who grants it, rather than offering a button that refuses', () => {
    const { container } = draw(secret, false)
    expect(screen.queryByRole('button', { name: /Reveal/ })).toBeNull()
    expect(container.textContent).toContain('password')
    expect(container.textContent).toContain('7 bytes')
    expect(container.textContent).toMatch(/super admin grants/)
  })
})
