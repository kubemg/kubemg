/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { AuditEvent } from '../api/types'
import { AuditRecordSheet } from './AuditRecordSheet'

/*
 * What a record has to say to be evidence: the whole path rather than the
 * table's ellipsised one, the two identities crossed, where the call came from,
 * and the guardrail decision where there was one. And what it must say when a
 * field is absent — "not recorded", never a blank cell that reads as an unknown
 * host.
 */

afterEach(cleanup)

function record(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 1,
    at: '2026-08-26T16:28:22Z',
    user_id: 4,
    username: 'devops',
    cluster_id: 2,
    cluster: 'prod-eu-west-1',
    verb: 'delete',
    method: 'DELETE',
    path: '/api/v1/namespaces/prod/pods/api-7f9c8d?gracePeriodSeconds=30',
    namespace: 'prod',
    resource: 'pods',
    impersonated_user: 'devops',
    impersonated_groups: ['kubemg:edit'],
    status: 200,
    duration_ms: 41,
    streaming: false,
    ...over,
  }
}

describe('an audit record, opened', () => {
  it('carries the whole path the table had to truncate', () => {
    render(<AuditRecordSheet event={record()} onClose={() => {}} />)
    expect(
      screen.getByText('/api/v1/namespaces/prod/pods/api-7f9c8d?gracePeriodSeconds=30'),
    ).toBeTruthy()
  })

  it('crosses the two identities', () => {
    // The KubeMG account on one side and the subject asserted to the API server
    // on the other. This is the record's crux and it had nowhere to be read.
    render(<AuditRecordSheet event={record()} onClose={() => {}} />)
    expect(screen.getByText('Impersonated as')).toBeTruthy()
    expect(screen.getByText('kubemg:edit')).toBeTruthy()
  })

  it('says where the call came from', () => {
    render(
      <AuditRecordSheet
        event={record({ source_addr: '203.0.113.7', user_agent: 'kubectl/v1.31.0' })}
        onClose={() => {}}
      />,
    )
    expect(screen.getByText('203.0.113.7')).toBeTruthy()
    expect(screen.getByText('kubectl/v1.31.0')).toBeTruthy()
  })

  it('says a missing source was not recorded, and why it cannot be filled in', () => {
    render(<AuditRecordSheet event={record()} onClose={() => {}} />)
    expect(screen.getAllByText('not recorded').length).toBeGreaterThan(0)
    expect(screen.getByText(/Neither can be filled in afterwards/)).toBeTruthy()
  })

  it('shows the guardrail decision only where a rule matched', () => {
    const { unmount } = render(<AuditRecordSheet event={record()} onClose={() => {}} />)
    expect(screen.queryByText('Guardrail')).toBeNull()
    unmount()

    render(
      <AuditRecordSheet
        event={record({ guardrail_policy: 'no prod deletes', guardrail_action: 'block' })}
        onClose={() => {}}
      />,
    )
    expect(screen.getByText('Guardrail')).toBeTruthy()
    expect(screen.getByText('no prod deletes')).toBeTruthy()
    expect(screen.getByText('block')).toBeTruthy()
  })

  it('offers a replay and a diff only when the row has one', () => {
    const { unmount } = render(<AuditRecordSheet event={record()} onClose={() => {}} />)
    expect(screen.queryByRole('button', { name: /Replay session/ })).toBeNull()
    unmount()

    render(<AuditRecordSheet event={record()} onClose={() => {}} onReplay={() => {}} />)
    expect(screen.getByRole('button', { name: /Replay session/ })).toBeTruthy()
  })

  it('states the instant in the deck’s own format rather than the browser’s', () => {
    render(<AuditRecordSheet event={record()} onClose={() => {}} />)
    // ISO order, 24-hour, zone stated — never `8/26/2026, 7:28:22 PM`.
    expect(screen.getByText(/^2026-08-\d\d \d\d:\d\d:\d\d UTC/)).toBeTruthy()
  })
})
