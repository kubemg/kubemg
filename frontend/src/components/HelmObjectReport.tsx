import type { HelmObjectReport as ObjectReport, HelmWriteResult } from '../api/types'
import type { Tone } from '../lib/status'
import { Notice, Pill, Row, Slab, Table, Td, Th } from './primitives'

/**
 * What a write to a Helm release actually did, object by object — the report
 * `helm install`/`helm upgrade` prints, rendered rather than left in a response
 * nobody reads.
 *
 * Every write that carries a chart (install, upgrade, a values edit or a
 * rollback against a release Helm itself wrote) answers with `objects`. The one
 * write that cannot render — a values edit against a release whose Secret was
 * written by something that stripped its chart — answers with `warning`
 * instead, and that is the only caveat left to show: there is no other case
 * left where a write here re-applies nothing.
 */
export function HelmObjectReport({ result }: { result: HelmWriteResult }) {
  if (!result.objects) {
    // The one remaining unrenderable case. `warning` is the backend's own
    // account of it rather than anything hard-coded here, so a build that
    // narrows or widens that case changes what this shows without a frontend
    // release.
    return result.warning ? <Notice tone="warn">{result.warning}</Notice> : null
  }

  return (
    <div className="flex flex-col gap-3">
      {result.applied === false ? (
        <Notice tone="error">{result.error ?? 'The write stopped partway through.'}</Notice>
      ) : (
        <Notice tone="ok">
          Revision {result.release.revision} is deployed.
        </Notice>
      )}
      {result.hook_notice ? <Notice tone="info">{result.hook_notice}</Notice> : null}

      <HelmObjectTable objects={result.objects} />

      {result.notes ? (
        <div className="flex flex-col gap-1.5">
          <span className="label">Notes</span>
          <Slab>{result.notes}</Slab>
        </div>
      ) : null}
    </div>
  )
}

/**
 * The per-object table itself, shared by every write that produces one. An
 * uninstall reports in exactly this shape — one line per object, the cluster's
 * own words where something refused — because it is exactly the same kind of
 * run: a set of impersonated calls, each answered on its own.
 */
export function HelmObjectTable({ objects }: { objects: ObjectReport[] }) {
  return (
    <Table resizeKey="kubemg_cols_helm_objects">
        <thead>
          <tr>
            <Th columnKey="kind">Kind</Th>
            <Th columnKey="name">Name</Th>
            <Th className="hidden md:table-cell" columnKey="namespace">
              Namespace
            </Th>
            <Th columnKey="action">Action</Th>
          </tr>
        </thead>
        <tbody>
          {objects.map((object, index) => (
            <Row key={`${object.kind}/${object.namespace ?? ''}/${object.name}/${index}`}>
              <Td className="font-mono">
                {object.kind}
                {object.hook ? (
                  <span className="ml-1.5 text-[11px] text-muted">hook</span>
                ) : null}
              </Td>
              <Td className="truncate font-mono">{object.name}</Td>
              <Td className="hidden truncate font-mono text-muted md:table-cell">
                {object.namespace ?? '—'}
              </Td>
              <Td>
                <Pill tone={actionTone(object.action)}>{object.action}</Pill>
                {object.message ? (
                  <p className="mt-1 text-[12px] leading-snug text-danger">{object.message}</p>
                ) : null}
              </Td>
            </Row>
          ))}
        </tbody>
      </Table>
  )
}

function actionTone(action: ObjectReport['action']): Tone {
  switch (action) {
    case 'created':
    case 'deleted':
      return 'accent'
    case 'configured':
      return 'ok'
    case 'unchanged':
    case 'skipped':
      return 'idle'
    case 'failed':
      return 'bad'
    default:
      return 'idle'
  }
}
