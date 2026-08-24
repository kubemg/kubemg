import { useEffect, useState } from 'react'
import { ShieldCheck, ShieldX, TriangleAlert } from 'lucide-react'
import {
  askAccessReview,
  errorMessage,
  fetchAccessReviewVerbs,
  fetchGrantIdentity,
} from '../api/client'
import type { AccessReviewResult, Cluster, GrantIdentity } from '../api/types'
import { Button, Field, Notice, Panel, Select, TextInput } from './primitives'
import { ALL_NAMESPACES } from '../lib/resources'

/**
 * "May this identity do this here?", asked of the cluster.
 *
 * The lists beside this one are an inventory: they show what is *written down*.
 * That is necessary and it is not sufficient, because the gap between the
 * bindings and the verdict is exactly where an audit finding lives —
 * aggregation assembles rules out of labels, a wildcard means more than it
 * looks like, one subject can be reached through three bindings at once, and the
 * cluster may be running an authorizer that is not RBAC at all. Deriving an
 * answer here by reading those tables would be KubeMG guessing at something the
 * cluster is willing to state outright.
 *
 * So this asks. A SubjectAccessReview is the authorizer's own verdict on a named
 * subject, and what comes back is quoted rather than interpreted.
 *
 * Two things about it shape this panel:
 *
 *   - The review is a `create` against `authorization.k8s.io`, so it is a
 *     **write in RBAC's eyes** despite changing nothing. A caller whose grant
 *     does not carry it is refused, and that refusal is shown as the cluster's
 *     own — it is a true and useful fact about the caller, not a broken panel.
 *   - "Allowed" and "not allowed" are not the whole answer space. An explicit
 *     *deny* cannot be overturned by adding a binding, and an *evaluation error*
 *     is not a denial at all. Both get their own reading, because collapsing
 *     either into "no" sends somebody off to write a RoleBinding that will not
 *     help.
 */

/** The resources people actually ask about, as a starting point rather than a limit. */
const COMMON_RESOURCES = [
  'pods',
  'pods/exec',
  'pods/log',
  'deployments',
  'statefulsets',
  'daemonsets',
  'jobs',
  'cronjobs',
  'services',
  'ingresses',
  'configmaps',
  'secrets',
  'namespaces',
  'nodes',
  'persistentvolumeclaims',
  'roles',
  'rolebindings',
  'clusterroles',
  'clusterrolebindings',
  'serviceaccounts',
]

/**
 * The API group a resource belongs to, for the ones a form can be sure about. A
 * SubjectAccessReview matches on the group as well as the name, and an empty
 * group means **core** rather than "any" — so asking about `deployments` with no
 * group asks about a core-group `deployments` that does not exist, and the
 * cluster correctly answers no. That is the single most confusing way to get a
 * wrong answer out of this, so the form fills it in.
 */
const RESOURCE_GROUPS: Record<string, string> = {
  deployments: 'apps',
  statefulsets: 'apps',
  daemonsets: 'apps',
  replicasets: 'apps',
  jobs: 'batch',
  cronjobs: 'batch',
  ingresses: 'networking.k8s.io',
  networkpolicies: 'networking.k8s.io',
  roles: 'rbac.authorization.k8s.io',
  rolebindings: 'rbac.authorization.k8s.io',
  clusterroles: 'rbac.authorization.k8s.io',
  clusterrolebindings: 'rbac.authorization.k8s.io',
  storageclasses: 'storage.k8s.io',
}

/** The verbs offered before the server has said which it accepts. */
const FALLBACK_VERBS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete']

/**
 * Splits `pods/exec` into the two fields the review actually has. A subresource
 * is a separate field in the API, but nobody thinks of it that way — `pods/exec`
 * is how it is written everywhere else, including in the Role it comes from.
 */
function splitResource(input: string): { resource: string; subresource: string } {
  const [resource, subresource = ''] = input.trim().split('/')
  return { resource, subresource }
}

export function AccessReviewPanel({
  cluster,
  namespace,
}: {
  cluster: Cluster
  /** The namespace Explore is open on, which is the one the question defaults to. */
  namespace: string
}) {
  const [identity, setIdentity] = useState<GrantIdentity | null>(null)
  const [verbs, setVerbs] = useState<string[]>(FALLBACK_VERBS)

  const [subject, setSubject] = useState('')
  const [verb, setVerb] = useState('get')
  const [resource, setResource] = useState('pods')
  const [group, setGroup] = useState('')
  // Whether the group field is being managed by the resource picker or by the
  // person. Once they type in it, the picker stops overwriting them — a form
  // that silently undoes an edit is worse than one that never helped.
  const [groupTouched, setGroupTouched] = useState(false)

  const [asking, setAsking] = useState(false)
  const [result, setResult] = useState<AccessReviewResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  // A cluster-scoped question is the "all namespaces" selection: the sentinel
  // never names a real namespace, so it becomes an empty namespace on the wire,
  // which is what a cluster-wide review is.
  const scope = namespace === ALL_NAMESPACES ? '' : namespace

  // Who KubeMG impersonates for this caller here. It is read rather than
  // assembled in the browser so the subject offered is the one actually put on
  // the wire — an identity page that disagreed with the impersonation would send
  // people chasing an answer to a question they did not ask.
  useEffect(() => {
    let cancelled = false
    setIdentity(null)
    fetchGrantIdentity(cluster.id)
      .then((answer) => {
        if (cancelled) return
        setIdentity(answer)
        // The field starts on the caller, because "what is my own grant worth
        // here" is the question this panel is opened with far more often than
        // any question about somebody else.
        setSubject((current) => current || answer.subject)
      })
      .catch(() => {
        // Not being able to resolve it is not worth an error: the subject is a
        // free text field and the panel works without the convenience.
      })
    return () => {
      cancelled = true
    }
  }, [cluster.id])

  useEffect(() => {
    let cancelled = false
    fetchAccessReviewVerbs(cluster.id)
      .then((answer) => {
        if (!cancelled && answer.length > 0) setVerbs(answer)
      })
      .catch(() => {
        /* The fallback list is a subset of what the server accepts. */
      })
    return () => {
      cancelled = true
    }
  }, [cluster.id])

  // A new question invalidates the last answer. Leaving it on screen while the
  // fields under it have changed is how a panel comes to state a verdict about
  // something nobody asked.
  useEffect(() => {
    setResult(null)
    setError(null)
  }, [subject, verb, resource, group, scope])

  function pickResource(next: string) {
    setResource(next)
    if (groupTouched) return
    const { resource: bare } = splitResource(next)
    setGroup(RESOURCE_GROUPS[bare] ?? '')
  }

  async function ask() {
    const { resource: bare, subresource } = splitResource(resource)
    if (!subject.trim() || !bare) return

    setAsking(true)
    setError(null)
    try {
      setResult(
        await askAccessReview(cluster.id, {
          subject: subject.trim(),
          // The groups the impersonated identity carries. An authorizer decides
          // on the union of a user and their groups, so a review that omitted
          // them would answer "no" exactly where the real request succeeds —
          // KubeMG's whole `view`/`edit`/`admin` model is group-based, and
          // dropping them here would make every answer about it wrong.
          groups: subject.trim() === identity?.subject ? identity.groups : undefined,
          verb,
          resource: bare,
          subresource: subresource || undefined,
          group: group.trim() || undefined,
          namespace: scope || undefined,
        }),
      )
    } catch (err) {
      setError(errorMessage(err, 'The cluster could not answer that.'))
    } finally {
      setAsking(false)
    }
  }

  return (
    <Panel
      eyebrow="The cluster's verdict"
      title="Can this identity do that?"
      description="Asked of the cluster's own authorizer as a SubjectAccessReview, not derived from the bindings below. Creating a review is itself a write in RBAC's eyes, so a grant that may not create one is refused — by the cluster, in its own words."
      bodyClassName="flex flex-col gap-4 p-4"
    >
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div className="sm:col-span-2">
          <Field
            label="Identity"
            htmlFor="review-subject"
            hint={
              identity && subject.trim() === identity.subject
                ? `You, as kubemg impersonates you here${
                    identity.groups.length > 0 ? ` (${identity.groups.join(', ')})` : ''
                  }`
                : 'A user, a group, or system:serviceaccount:<namespace>:<name>'
            }
          >
            <TextInput
              id="review-subject"
              value={subject}
              onChange={(event) => setSubject(event.target.value)}
              placeholder="system:serviceaccount:payments:deployer"
              className="font-mono text-[12.5px]"
            />
          </Field>
        </div>

        <Field label="Verb" htmlFor="review-verb">
          <Select
            id="review-verb"
            value={verb}
            onChange={(event) => setVerb(event.target.value)}
          >
            {verbs.map((entry) => (
              <option key={entry} value={entry}>
                {entry}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="Resource"
          htmlFor="review-resource"
          hint="A subresource is written the way a Role writes it: pods/exec."
        >
          <TextInput
            id="review-resource"
            list="review-resources"
            value={resource}
            onChange={(event) => pickResource(event.target.value)}
            placeholder="pods"
            className="font-mono text-[12.5px]"
          />
          <datalist id="review-resources">
            {COMMON_RESOURCES.map((entry) => (
              <option key={entry} value={entry} />
            ))}
          </datalist>
        </Field>

        <Field
          label="API group"
          htmlFor="review-group"
          hint="Empty means the core group, not any group — which is why picking a known resource fills this in."
        >
          <TextInput
            id="review-group"
            value={group}
            onChange={(event) => {
              setGroupTouched(true)
              setGroup(event.target.value)
            }}
            placeholder="apps"
            className="font-mono text-[12.5px]"
          />
        </Field>

        <div className="flex items-end sm:col-span-2 lg:col-span-1">
          <Button
            variant="primary"
            onClick={() => void ask()}
            disabled={asking || !subject.trim() || !resource.trim()}
          >
            {asking ? 'Asking the cluster…' : 'Ask the cluster'}
          </Button>
        </div>
      </div>

      <p className="text-[12px] text-muted">
        {scope ? (
          <>
            Asked in <span className="font-mono">{scope}</span> — the namespace this page is open
            on.
          </>
        ) : (
          'Asked cluster-wide, because this page is open across every namespace.'
        )}
      </p>

      {error ? <Notice tone="error">{error}</Notice> : null}

      {result ? <AccessReviewVerdict result={result} /> : null}
    </Panel>
  )
}

/**
 * The answer, quoted. Four outcomes rather than two, and the two extra ones are
 * the ones worth building a component for: an explicit deny that a new binding
 * will not fix, and an evaluation error that is not a denial at all.
 */
function AccessReviewVerdict({ result }: { result: AccessReviewResult }) {
  const question = (
    <>
      <span className="font-mono">{result.subject}</span>
      {' — '}
      <span className="font-mono">
        {result.verb} {result.resource}
      </span>
      {result.namespace ? (
        <>
          {' in '}
          <span className="font-mono">{result.namespace}</span>
        </>
      ) : (
        ' cluster-wide'
      )}
    </>
  )

  if (result.evaluation_error) {
    return (
      <div className="flex flex-col gap-2">
        <Notice tone="warn">
          <span className="flex items-start gap-2">
            <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <span>
              The authorizer could not decide. That is not a denial, and treating it as one would
              be wrong about the cluster.
            </span>
          </span>
        </Notice>
        <p className="text-[12.5px] text-muted">{question}</p>
        <p className="font-mono text-[12px] text-muted">{result.evaluation_error}</p>
      </div>
    )
  }

  const tone = result.allowed ? 'ok' : result.denied ? 'error' : 'info'
  const Icon = result.allowed ? ShieldCheck : ShieldX

  return (
    <div className="flex flex-col gap-2">
      <Notice tone={tone}>
        <span className="flex items-start gap-2">
          <Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>
            {result.allowed
              ? 'Allowed.'
              : result.denied
                ? 'Explicitly denied — an authorizer refused it outright, so adding a binding will not change this.'
                : 'Not allowed. No rule grants it; nothing denies it either.'}{' '}
            {question}
          </span>
        </span>
      </Notice>
      {/* The authorizer's own explanation, which usually names the binding that
          decided it — the one piece of this that no inventory could produce. */}
      {result.reason ? (
        <p className="font-mono text-[12px] leading-relaxed text-muted">{result.reason}</p>
      ) : null}
    </div>
  )
}
