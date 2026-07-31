import { useCallback, useEffect, useMemo, useState } from 'react'
import { Pencil, Plus, ShieldAlert, ShieldCheck, Sparkles, Trash2 } from 'lucide-react'
import {
  createGuardrailPolicy,
  deleteGuardrailPolicy,
  errorMessage,
  fetchGuardrailPolicies,
  fetchGuardrailTemplates,
  updateGuardrailPolicy,
} from '../../api/client'
import type {
  Cluster,
  GuardrailAction,
  GuardrailPolicy,
  GuardrailPolicyInput,
  GuardrailTarget,
  GuardrailTemplate,
} from '../../api/types'
import {
  Button,
  Field,
  IconButton,
  Notice,
  Panel,
  Pill,
  Select,
  Sheet,
  TextArea,
  TextInput,
} from '../primitives'

/**
 * Command guardrails: the calls this platform refuses to pass on.
 *
 * Everything else in the console configures who may do what, and the cluster's
 * own RBAC settles it. This panel is the opposite: it stops people who *are*
 * entitled, because `kubectl delete ns prod` succeeds precisely for the person
 * with the privilege to run it.
 *
 * So the panel leads with scope. A rule is either fleet-wide — including clusters
 * registered next month — or attached to one cluster, and which of the two it is
 * changes the blast radius of getting it wrong. That is the badge on every row
 * and the first field in the editor.
 */

const TARGET_LABEL: Record<GuardrailTarget, string> = {
  api_request: 'API calls',
  terminal_exec: 'Commands',
  both: 'API calls & commands',
}

const TARGET_HINT: Record<GuardrailTarget, string> = {
  api_request:
    'Matched against “METHOD /path” on a proxied call — so `^DELETE /api/v1/namespaces/[^/?]+$` is a rule about deleting namespaces.',
  terminal_exec:
    'Matched against each line typed into a container, and against the argv of a non-interactive `kubectl exec -- …`.',
  both: 'Matched against both subjects. Right for a pattern that reads naturally against either, and wrong for most.',
}

const ACTION_HINT: Record<GuardrailAction, string> = {
  block: 'Refuse the call. An API request gets a 403; a typed command never reaches the shell.',
  warn: 'Let it through and record the match. Run a new rule this way for a week, read the trail, then arm it.',
}

const EMPTY: GuardrailPolicyInput = {
  name: '',
  description: '',
  cluster_id: 0,
  pattern: '',
  target: 'api_request',
  action: 'block',
  enabled: true,
}

/** A sample subject to try a pattern against, per target, so the tester starts
    with something shaped like what the rule will really see. */
const SAMPLE: Record<GuardrailTarget, string> = {
  api_request: 'DELETE /api/v1/namespaces/prod',
  terminal_exec: 'rm -rf /',
  both: 'DELETE /api/v1/namespaces/prod',
}

export function GuardrailSettingsPanel({ clusters }: { clusters: Cluster[] }) {
  const [policies, setPolicies] = useState<GuardrailPolicy[]>([])
  const [enforcing, setEnforcing] = useState(0)
  const [templates, setTemplates] = useState<GuardrailTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [editing, setEditing] = useState<GuardrailPolicy | GuardrailPolicyInput | null>(null)
  const [browsing, setBrowsing] = useState(false)
  const [busyRow, setBusyRow] = useState<number | null>(null)

  const load = useCallback(async () => {
    try {
      const [list, presets] = await Promise.all([
        fetchGuardrailPolicies(),
        fetchGuardrailTemplates(),
      ])
      setPolicies(list.policies)
      setEnforcing(list.enforcing)
      setTemplates(presets)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the guardrail policies.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const armed = useMemo(() => policies.filter((policy) => policy.enabled).length, [policies])

  /** A rule whose pattern stopped compiling is skipped by the gateway while the
      row still reads as armed. The count is the only place that gap is visible. */
  const silent = armed - enforcing

  async function toggle(policy: GuardrailPolicy) {
    setBusyRow(policy.id)
    try {
      await updateGuardrailPolicy(policy.id, {
        name: policy.name,
        description: policy.description ?? '',
        cluster_id: policy.cluster_id,
        pattern: policy.pattern,
        target: policy.target,
        action: policy.action,
        enabled: !policy.enabled,
      })
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not change the rule.'))
    } finally {
      setBusyRow(null)
    }
  }

  async function remove(policy: GuardrailPolicy) {
    if (!window.confirm(`Delete the guardrail “${policy.name}”?`)) return
    try {
      await deleteGuardrailPolicy(policy.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not delete the rule.'))
    }
  }

  return (
    <>
      <Panel
        eyebrow="Safety"
        title="Command guardrails"
        description="Rules that refuse a call whatever the cluster’s RBAC allows — because the person who deletes the wrong namespace is usually the person entitled to delete it. They cover proxied API calls and what is typed into a container."
        bodyClassName="flex flex-col"
        actions={
          <>
            <Button size="sm" variant="ghost" onClick={() => setBrowsing(true)}>
              <Sparkles aria-hidden="true" className="size-3.5" />
              Presets
            </Button>
            <Button size="sm" onClick={() => setEditing({ ...EMPTY })}>
              <Plus aria-hidden="true" className="size-3.5" />
              Add rule
            </Button>
          </>
        }
      >
        {error ? (
          <div className="px-4 pt-4">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        {silent > 0 ? (
          <div className="px-4 pt-4">
            <Notice tone="warn">
              {silent} enabled {silent === 1 ? 'rule is' : 'rules are'} not being enforced, because
              the pattern no longer compiles. Check the server log and re-save the rule — until then
              it reads as armed and stops nothing.
            </Notice>
          </div>
        ) : null}

        {loading ? <p className="px-4 py-6 text-[13px] text-muted">Loading…</p> : null}

        {!loading && policies.length === 0 ? (
          <p className="px-4 py-6 text-[13px] text-muted">
            No guardrails yet. Start from a preset — the rules worth having on day one are written
            there already.
          </p>
        ) : null}

        <ul className="divide-y divide-line-soft">
          {policies.map((policy) => (
            <li key={policy.id} className="flex flex-wrap items-start gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-[13.5px] font-medium text-fg">{policy.name}</span>

                  {/* Scope first: it is what decides the blast radius. */}
                  {policy.cluster_id === 0 ? (
                    <Pill tone="accent">Global</Pill>
                  ) : (
                    <Pill tone="idle">
                      Cluster: {policy.cluster_name || `#${policy.cluster_id}`}
                    </Pill>
                  )}

                  <Pill tone={policy.action === 'block' ? 'bad' : 'warn'}>{policy.action}</Pill>
                  {policy.enabled ? null : <Pill tone="idle">disabled</Pill>}
                </div>

                <p className="mt-1 truncate font-mono text-[12px] text-muted">{policy.pattern}</p>
                <p className="mt-1 text-[12px] text-faint">
                  {TARGET_LABEL[policy.target]}
                  {policy.description ? ` · ${policy.description}` : ''}
                </p>
              </div>

              <div className="flex shrink-0 items-center gap-1.5">
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busyRow === policy.id}
                  onClick={() => void toggle(policy)}
                >
                  {policy.enabled ? (
                    <ShieldCheck aria-hidden="true" className="size-3.5" />
                  ) : (
                    <ShieldAlert aria-hidden="true" className="size-3.5" />
                  )}
                  {policy.enabled ? 'Disable' : 'Enable'}
                </Button>
                <IconButton label="Edit rule" onClick={() => setEditing(policy)}>
                  <Pencil aria-hidden="true" className="size-4" />
                </IconButton>
                <IconButton label="Delete rule" onClick={() => void remove(policy)}>
                  <Trash2 aria-hidden="true" className="size-4" />
                </IconButton>
              </div>
            </li>
          ))}
        </ul>

        {/* What a guardrail is not. Saying so here is the honest version of the
            feature: anyone who can open a shell can defeat a pattern, and a
            control oversold as a sandbox is one somebody relies on wrongly. */}
        <div className="px-4 py-3">
          <Notice tone="info">
            Guardrails stop the right command typed against the wrong cluster. They are not a
            sandbox: anyone who can open a shell can work around a pattern, and the controls for
            that are the grant that let them in and the recording of what they did.
          </Notice>
        </div>
      </Panel>

      {editing ? (
        <GuardrailSheet
          policy={editing}
          clusters={clusters}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void load()
          }}
        />
      ) : null}

      {browsing ? (
        <PresetSheet
          templates={templates}
          onClose={() => setBrowsing(false)}
          onPick={(template) => {
            setBrowsing(false)
            // A preset opens the editor rather than saving straight away: the
            // scope is the one thing no catalogue can decide for an operator.
            setEditing({
              name: template.name,
              description: template.description,
              cluster_id: 0,
              pattern: template.pattern,
              target: template.target,
              action: template.action,
              enabled: true,
            })
          }}
        />
      ) : null}
    </>
  )
}

/* ----------------------------------------------------------------- edit --- */

function isStored(policy: GuardrailPolicy | GuardrailPolicyInput): policy is GuardrailPolicy {
  return 'id' in policy
}

function GuardrailSheet({
  policy,
  clusters,
  onClose,
  onSaved,
}: {
  policy: GuardrailPolicy | GuardrailPolicyInput
  clusters: Cluster[]
  onClose: () => void
  onSaved: () => void
}) {
  const stored = isStored(policy) ? policy : null
  const [draft, setDraft] = useState<GuardrailPolicyInput>({
    name: policy.name,
    description: policy.description ?? '',
    cluster_id: policy.cluster_id,
    pattern: policy.pattern,
    target: policy.target,
    action: policy.action,
    enabled: policy.enabled ?? true,
  })
  const [sample, setSample] = useState(SAMPLE[policy.target])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function set<K extends keyof GuardrailPolicyInput>(key: K, value: GuardrailPolicyInput[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  /**
   * The tester. A guardrail is a regular expression whose only feedback would
   * otherwise be a colleague being refused at some point in the future, which is
   * the worst possible time to discover a typo.
   *
   * The server is the authority — it compiles with RE2 — so this is a preview,
   * and it says so. For the shapes these rules take the two agree.
   */
  const preview = useMemo(() => {
    const pattern = draft.pattern.trim()
    if (!pattern) return { valid: false, matches: false, problem: 'A pattern is required.' }
    try {
      const re = new RegExp(pattern)
      // The rule the server enforces too: a pattern matching the empty string
      // matches every subject there will ever be.
      if (re.test('')) {
        return {
          valid: false,
          matches: false,
          problem:
            'This matches everything, which would block every request on every cluster it covers.',
        }
      }
      return { valid: true, matches: re.test(sample), problem: null }
    } catch (err) {
      return { valid: false, matches: false, problem: (err as Error).message }
    }
  }, [draft.pattern, sample])

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const payload: GuardrailPolicyInput = {
        ...draft,
        name: draft.name.trim(),
        description: draft.description?.trim() ?? '',
        pattern: draft.pattern.trim(),
      }
      if (stored) {
        await updateGuardrailPolicy(stored.id, payload)
      } else {
        await createGuardrailPolicy(payload)
      }
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the rule.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      width="lg"
      eyebrow="Safety"
      title={stored ? `Edit ${stored.name}` : 'New guardrail'}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !draft.name.trim() || !preview.valid}
            onClick={() => void save()}
          >
            {busy ? 'Saving…' : 'Save rule'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Field label="Name" htmlFor="guardrail-name">
          <TextInput
            id="guardrail-name"
            value={draft.name}
            placeholder="Block namespace deletion"
            onChange={(event) => set('name', event.target.value)}
          />
        </Field>

        {/* Scope is first because it is the decision with consequences. */}
        <Field
          label="Scope"
          htmlFor="guardrail-scope"
          hint="A fleet-wide rule also covers clusters registered later, which is what no per-cluster rule can keep up with."
        >
          <Select
            id="guardrail-scope"
            value={String(draft.cluster_id)}
            onChange={(event) => set('cluster_id', Number(event.target.value))}
          >
            <option value="0">Global (all clusters)</option>
            {clusters.map((cluster) => (
              <option key={cluster.id} value={String(cluster.id)}>
                {cluster.name} ({cluster.environment})
              </option>
            ))}
          </Select>
        </Field>

        <Field label="Applies to" htmlFor="guardrail-target" hint={TARGET_HINT[draft.target]}>
          <Select
            id="guardrail-target"
            value={draft.target}
            onChange={(event) => {
              const target = event.target.value as GuardrailTarget
              set('target', target)
              setSample(SAMPLE[target])
            }}
          >
            <option value="api_request">{TARGET_LABEL.api_request}</option>
            <option value="terminal_exec">{TARGET_LABEL.terminal_exec}</option>
            <option value="both">{TARGET_LABEL.both}</option>
          </Select>
        </Field>

        <Field label="Action" htmlFor="guardrail-action" hint={ACTION_HINT[draft.action]}>
          <Select
            id="guardrail-action"
            value={draft.action}
            onChange={(event) => set('action', event.target.value as GuardrailAction)}
          >
            <option value="block">Block</option>
            <option value="warn">Warn only</option>
          </Select>
        </Field>

        <Field
          label="Pattern"
          htmlFor="guardrail-pattern"
          hint="A regular expression, matched anywhere in the subject unless anchored with ^ and $."
        >
          <TextInput
            id="guardrail-pattern"
            className="font-mono text-[12.5px]"
            value={draft.pattern}
            placeholder="^DELETE /api/v1/namespaces/[^/?]+$"
            onChange={(event) => set('pattern', event.target.value)}
          />
        </Field>

        {/* Try it before somebody else does. */}
        <Field
          label="Try it"
          htmlFor="guardrail-sample"
          hint="A preview in your browser. The server compiles the pattern itself and is the authority."
        >
          <TextInput
            id="guardrail-sample"
            className="font-mono text-[12.5px]"
            value={sample}
            onChange={(event) => setSample(event.target.value)}
          />
        </Field>

        {preview.problem ? (
          <Notice tone="error">{preview.problem}</Notice>
        ) : (
          <p
            className={`flex items-baseline gap-1.5 rounded-control bg-raised px-3 py-2 text-[12.5px] ${
              preview.matches ? 'text-danger' : 'text-muted'
            }`}
          >
            {preview.matches ? (
              <ShieldAlert aria-hidden="true" className="size-3.5 translate-y-0.5 shrink-0" />
            ) : (
              <ShieldCheck aria-hidden="true" className="size-3.5 translate-y-0.5 shrink-0" />
            )}
            {preview.matches
              ? `This sample would be ${draft.action === 'block' ? 'refused' : 'flagged'}.`
              : 'This sample would pass.'}
          </p>
        )}

        <Field
          label="Why this rule exists"
          htmlFor="guardrail-description"
          hint="Shown to whoever meets the rule. They are being told “no” by a colleague who is not in the room."
        >
          <TextArea
            id="guardrail-description"
            rows={3}
            value={draft.description}
            onChange={(event) => set('description', event.target.value)}
          />
        </Field>

        <label className="flex items-baseline gap-2 text-[13px] text-fg">
          <input
            type="checkbox"
            className="translate-y-0.5"
            checked={draft.enabled ?? true}
            onChange={(event) => set('enabled', event.target.checked)}
          />
          Enforce this rule
        </label>
      </div>
    </Sheet>
  )
}

/* -------------------------------------------------------------- presets --- */

function PresetSheet({
  templates,
  onClose,
  onPick,
}: {
  templates: GuardrailTemplate[]
  onClose: () => void
  onPick: (template: GuardrailTemplate) => void
}) {
  return (
    <Sheet
      width="lg"
      eyebrow="Safety"
      title="Preset guardrails"
      onClose={onClose}
      footer={
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <Notice tone="info">
          Each preset opens the editor rather than saving straight away — the scope is the one thing
          a catalogue cannot decide for you.
        </Notice>

        {templates.map((template) => (
          <div key={template.key} className="rounded-card border border-line-soft p-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[13.5px] font-medium text-fg">{template.name}</span>
              <Pill tone={template.action === 'block' ? 'bad' : 'warn'}>{template.action}</Pill>
              <Pill tone="idle">{TARGET_LABEL[template.target]}</Pill>
            </div>
            <p className="mt-1 text-[12.5px] leading-relaxed text-muted">{template.description}</p>
            <p className="mt-2 break-all font-mono text-[12px] text-faint">{template.pattern}</p>
            <div className="mt-2">
              <Button size="sm" onClick={() => onPick(template)}>
                Use this
              </Button>
            </div>
          </div>
        ))}
      </div>
    </Sheet>
  )
}
