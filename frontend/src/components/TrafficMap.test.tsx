/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { act } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { TrafficMap, TrafficNode } from '../api/types'
import type { DetailTarget } from './ResourceDetailDrawer'
import { TrafficMapView } from './TrafficMap'

/*
 * The map is only a navigation surface where a hop is an object this console
 * can open. A host, a mesh or an external API is drawn but is not a button —
 * offering a click that goes nowhere is worse than not offering it.
 */

const map: TrafficMap = {
  root: 'ingresses/shop/web',
  nodes: [
    { id: 'host/shop.example.com', kind: 'Host', name: 'shop.example.com', column: 0, detail: ['TLS · secret shop-tls'], state: 'ok' },
    { id: 'ingresses/shop/web', kind: 'Ingress', resource: 'ingresses', namespace: 'shop', name: 'web', column: 1, detail: [], state: 'ok' },
    {
      id: 'services/shop/api',
      kind: 'Service',
      resource: 'services',
      namespace: 'shop',
      name: 'api',
      column: 2,
      detail: [],
      state: 'bad',
      problem: 'its selector app=api matches no pods',
    },
  ],
  edges: [
    { from: 'host/shop.example.com', to: 'ingresses/shop/web', labels: [], state: 'ok' },
    { from: 'ingresses/shop/web', to: 'services/shop/api', labels: ['shop.example.com/api:80'], state: 'ok' },
  ],
  notes: ['A subset is drawn as a label: DestinationRules are not read.'],
}

function targetOf(node: TrafficNode): DetailTarget | null {
  if (!node.resource) return null
  return { kind: node.resource as DetailTarget['kind'], label: node.kind, name: node.name, namespace: node.namespace }
}

function draw(onOpen = vi.fn(), onFocus = vi.fn()) {
  render(
    <TrafficMapView map={map} focus={null} onFocus={onFocus} targetOf={targetOf} onOpen={onOpen} />,
  )
  return { onOpen, onFocus }
}

afterEach(cleanup)

describe('the traffic map', () => {
  it('opens a hop that is an object in the drawer', () => {
    const { onOpen } = draw()
    fireEvent.click(screen.getByRole('button', { name: /^Service api, broken/ }))
    expect(onOpen).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'services', name: 'api', namespace: 'shop' }),
    )
  })

  it('opens one from the keyboard too', () => {
    const { onOpen } = draw()
    fireEvent.keyDown(screen.getByRole('button', { name: /^Ingress web/ }), { key: 'Enter' })
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ kind: 'ingresses', name: 'web' }))
  })

  it('does not offer a host as something to open', () => {
    draw()
    expect(screen.queryByRole('button', { name: /shop\.example\.com/ })).toBeNull()
  })

  it('says what is broken in words, beside the drawing', () => {
    draw()
    expect(screen.getByText('1 hop is broken on this map.', { exact: false })).toBeTruthy()
    expect(screen.getAllByText('its selector app=api matches no pods').length).toBeGreaterThan(0)
  })

  it('traces a hop’s path when it is pointed at', () => {
    const { onFocus } = draw()
    fireEvent.mouseEnter(screen.getByRole('button', { name: /^Service api/ }))
    expect(onFocus).toHaveBeenCalledWith('services/shop/api')
  })

  it('states what the map did not look at', () => {
    draw()
    expect(screen.getByText(/DestinationRules are not read/)).toBeTruthy()
  })
})

describe('pointing at hops', () => {
  it('moves the trace from one box to the next without clearing it in between', () => {
    vi.useFakeTimers()
    try {
      const { onFocus } = draw()
      const service = screen.getByRole('button', { name: /^Service api/ })
      const ingress = screen.getByRole('button', { name: /^Ingress web/ })
      fireEvent.mouseEnter(service)
      fireEvent.mouseLeave(service)
      fireEvent.mouseEnter(ingress)
      act(() => vi.advanceTimersByTime(500))
      // Clearing in the gap is what faded the whole map up and back down.
      expect(onFocus).not.toHaveBeenCalledWith(null)
      expect(onFocus).toHaveBeenLastCalledWith('ingresses/shop/web')
    } finally {
      vi.useRealTimers()
    }
  })

  it('clears the trace once the pointer has really left', () => {
    vi.useFakeTimers()
    try {
      const { onFocus } = draw()
      const service = screen.getByRole('button', { name: /^Service api/ })
      fireEvent.mouseEnter(service)
      fireEvent.mouseLeave(service)
      act(() => vi.advanceTimersByTime(500))
      expect(onFocus).toHaveBeenLastCalledWith(null)
    } finally {
      vi.useRealTimers()
    }
  })

  it('offers to narrow to what needs a look only where that hides something', () => {
    // Every hop of the base map is on the broken path, so narrowing would hide
    // nothing and is not offered.
    draw()
    expect(screen.queryByRole('button', { name: 'Only what needs a look' })).toBeNull()
    cleanup()

    const wider: TrafficMap = {
      ...map,
      nodes: [
        ...map.nodes,
        { id: 'host/docs.example.com', kind: 'Host', name: 'docs.example.com', column: 0, detail: [], state: 'ok' },
        { id: 'ingresses/shop/docs', kind: 'Ingress', resource: 'ingresses', namespace: 'shop', name: 'docs', column: 1, detail: [], state: 'ok' },
      ],
      edges: [...map.edges, { from: 'host/docs.example.com', to: 'ingresses/shop/docs', labels: [], state: 'ok' }],
    }
    render(<TrafficMapView map={wider} focus={null} onFocus={vi.fn()} targetOf={targetOf} onOpen={vi.fn()} />)
    expect(screen.getByRole('button', { name: /^Ingress docs/ })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Only what needs a look' }))
    expect(screen.queryByRole('button', { name: /^Ingress docs/ })).toBeNull()
    expect(screen.getByRole('button', { name: /^Service api/ })).toBeTruthy()
  })
})
