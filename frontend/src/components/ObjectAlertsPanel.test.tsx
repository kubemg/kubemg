/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AlarmCatalogue, AlarmInput, Cluster, FiringAlert } from '../api/types'
import { invalidateQueries } from '../lib/query'
import { AlarmComposer } from './AlarmComposer'
import { ObjectAlertsPanel } from './ObjectAlertsPanel'

// What the drawer owes an operator looking at an unhealthy object: what is
// firing for it, a Silence only where their grant allows one, and a Create
// alarm that sends a condition — never an expression — for exactly this object.

let firing: FiringAlert[] = []
let alertsError: unknown = null
const created: AlarmInput[] = []
const silenced: { fingerprint: string; duration: string; comment: string }[] = []

const catalogue: AlarmCatalogue = {
  conditions: {
    deployments: [
      { key: 'unavailable', label: 'Replicas unavailable', description: 'd', default_for: '5m', default_severity: 'warning' },
      {
        key: 'restarts',
        label: 'Pods restarting',
        description: 'r',
        default_for: '0m',
        default_severity: 'warning',
        threshold: { label: 'Restarts in 15 minutes', default: 3, min: 1, max: 1000 },
      },
    ],
  },
  durations: ['0m', '1m', '5m', '10m'],
  severities: ['info', 'warning', 'critical'],
}

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    fetchFiringAlerts: () =>
      alertsError ? Promise.reject(alertsError) : Promise.resolve({ alerts: firing, endpoint: 'x', can_silence: true }),
    fetchAlarms: () => Promise.resolve({ items: [], available: true }),
    fetchAlarmCatalogue: () => Promise.resolve(catalogue),
    createAlarm: (_id: number, input: AlarmInput) => {
      created.push(input)
      return Promise.resolve({ ...input, condition_label: input.condition, target: input.name, expr: '', created_at: '' })
    },
    createSilence: (_id: number, fingerprint: string, duration: string, comment: string) => {
      silenced.push({ fingerprint, duration, comment })
      return Promise.resolve('s1')
    },
  }
})
vi.mock('../state/confirm-context', () => ({ useConfirm: () => async () => true }))
vi.mock('../state/result-context', () => ({ useResult: () => () => {} }))

const cluster = { id: 4, name: 'edge' } as Cluster
const target = { kind: 'deployments', label: 'Deployment', name: 'api', namespace: 'shop' }

function alert(over: Partial<FiringAlert>): FiringAlert {
  return {
    fingerprint: 'a1',
    name: 'KubePodCrashLooping',
    namespace: 'shop',
    severity: 'warning',
    state: 'firing',
    starts_at: new Date().toISOString(),
    labels: {},
    annotations: { summary: 'api-7f9c5-x2kq9 is crash looping' },
    kubemg: false,
    can_silence: true,
    ...over,
  }
}

beforeEach(() => {
  invalidateQueries()
  firing = []
  alertsError = null
  created.length = 0
  silenced.length = 0
})
afterEach(cleanup)

describe('ObjectAlertsPanel', () => {
  it('silences a firing alert with a reason, and offers it only where the grant allows', async () => {
    firing = [alert({}), alert({ fingerprint: 'a2', can_silence: false, annotations: { summary: 'other' } })]
    render(<ObjectAlertsPanel cluster={cluster} target={target} onEdit={() => {}} />)

    expect(await screen.findByText('api-7f9c5-x2kq9 is crash looping')).toBeTruthy()
    const buttons = screen.getAllByRole('button', { name: /Silence/ })
    expect(buttons).toHaveLength(1)

    fireEvent.click(buttons[0])
    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'rolling out a fix' } })
    fireEvent.change(screen.getByLabelText('Silence duration'), { target: { value: '1h' } })
    fireEvent.submit(screen.getByRole('form', { name: 'Silence alert' }))
    await waitFor(() => expect(silenced).toHaveLength(1))
    expect(silenced[0]).toEqual({ fingerprint: 'a1', duration: '1h', comment: 'rolling out a fix' })
  })

  it('says there is no Alertmanager rather than that nothing is firing', async () => {
    alertsError = Object.assign(new Error('no'), {
      isAxiosError: true,
      response: { status: 404, data: { unconfigured: true } },
    })
    render(<ObjectAlertsPanel cluster={cluster} target={target} onEdit={() => {}} />)
    expect(await screen.findByText(/No Alertmanager is registered/)).toBeTruthy()
    expect(screen.queryByText(/Nothing is firing/)).toBeNull()
  })
})

describe('AlarmComposer', () => {
  it('opens on the condition that describes the object, and sends it for this object only', async () => {
    const onSaved = vi.fn()
    render(
      <AlarmComposer
        cluster={cluster}
        target={target}
        conditions={[{ type: 'Available', status: 'False' }]}
        onClose={() => {}}
        onSaved={onSaved}
      />,
    )
    const unavailable = (await screen.findByLabelText(/Replicas unavailable/)) as HTMLInputElement
    expect(unavailable.checked).toBe(true)

    fireEvent.click(screen.getByLabelText(/Pods restarting/))
    fireEvent.change(screen.getByLabelText('Restarts in 15 minutes'), { target: { value: '5' } })
    fireEvent.click(screen.getByRole('button', { name: 'Critical' }))
    fireEvent.submit(screen.getByRole('form', { name: 'Create alarm' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(created[0]).toEqual({
      namespace: 'shop',
      kind: 'deployments',
      name: 'api',
      condition: 'restarts',
      threshold: 5,
      for: '0m',
      severity: 'critical',
      note: undefined,
    })
  })
})
