import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Activity, FileKey, KeyRound, Timer, UsersRound } from 'lucide-react'
import {
  errorMessage,
  fetchIssuedKubeconfigs,
  fetchTerminalSessions,
  fetchUserAccess,
} from '../api/client'
import type { IssuedKubeconfig, TerminalSession, UserAccessReview } from '../api/types'
import { AppShell } from '../components/AppShell'
import {
  EmptyState,
  EnvironmentTag,
  Notice,
  Panel,
  Pill,
  Row,
  Table,
  Td,
  Th,
} from '../components/primitives'
import { railChip } from '../lib/branding'
import { formatInstant, relativeAge } from '../lib/time'

/**
 * One page for "what can this person reach today".
 *
 * An access review is a recurring, dated, evidenced obligation at any enterprise
 * running this product, and until now it meant assembling five screens by hand
 * per person: the permissions matrix for direct grants, the group list for
 * inherited ones, the JIT queue for live elevations, the credential register for
 * issued kubeconfigs, and the session index for what was actually done. The
 * facts were all here. The join was not.
 *
 * The page is ordered the way the review is walked: **who they are** (and which
 * directory says so), **what they can reach** and why, **what they hold** — a
 * kubeconfig on a laptop outlives the session that made it — and **what they
 * did**. Production sorts first throughout, because the rows that decide whether
 * a review is signed are the ones on the clusters that matter.
 *
 * Two things it deliberately does not claim. There is no MFA state, because
 * KubeMG has none: a federated account's second factor is the identity
 * provider's business and this console never sees it, and inventing a column
 * that read "unknown" for every row would be worse than the absence. And the
 * effective grant per cluster is the **server's** merge, not a second one
 * computed here — see the backend's user_access.go for why a page free to
 * disagree with the proxy would be worse than no page.
 */
export function UserDetail() {
  const params = useParams<{ id: string }>()
  const userId = Number(params.id)

  const [review, setReview] = useState<UserAccessReview | null>(null)
  const [credentials, setCredentials] = useState<IssuedKubeconfig[]>([])
  const [sessions, setSessions] = useState<TerminalSession[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let live = true
    setLoading(true)
    setError(null)

    // The review is the page; the other two are sections of it. So a failure to
    // read the review is the page's failure, while a failure to read a section
    // leaves that section empty rather than taking the whole review down — the
    // grants are the part somebody signs, and they must not be withheld because
    // the session index was slow.
    void fetchUserAccess(userId)
      .then((answer) => {
        if (live) setReview(answer)
      })
      .catch((err) => {
        if (live) setError(errorMessage(err, 'Could not read this account.'))
      })
      .finally(() => {
        if (live) setLoading(false)
      })

    void fetchIssuedKubeconfigs({ userId })
      .then((rows) => {
        if (live) setCredentials(rows)
      })
      .catch(() => undefined)

    void fetchTerminalSessions({ user_id: userId, limit: 10 })
      .then((page) => {
        if (live) setSessions(page.sessions ?? [])
      })
      .catch(() => undefined)

    return () => {
      live = false
    }
  }, [userId])

  const liveCredentials = useMemo(
    () => credentials.filter((row) => row.status === 'active').length,
    [credentials],
  )

  const title = review?.user.username ?? 'Account'

  return (
    <AppShell title={title} parent={{ label: 'Users', to: '/admin/users' }}>
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {loading && !review ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {review ? (
          <>
            <Identity review={review} />
            <Reach review={review} />
            <Credentials rows={credentials} live={liveCredentials} />
            <Sessions rows={sessions} />
          </>
        ) : null}
      </div>
    </AppShell>
  )
}

/**
 * Who this is, and which system says so.
 *
 * `auth_source` says *that* an account is federated; the provider's name says
 * which directory owns the identity, which is the question an auditor actually
 * asks and which otherwise means matching an id against the SSO settings page by
 * hand.
 */
function Identity({ review }: { review: UserAccessReview }) {
  const { user } = review

  return (
    <Panel
      eyebrow="Identity"
      title={user.username}
      description="Where this account's credentials live, and when it was last used."
      bodyClassName="grid gap-4 p-4 sm:grid-cols-2 xl:grid-cols-4"
      actions={
        <>
          {user.is_active ? <Pill tone="ok">Active</Pill> : <Pill tone="warn">Disabled</Pill>}
          <Pill tone="idle">{user.system_role}</Pill>
        </>
      }
    >
      <Fact label="Email" value={user.email || 'None recorded'} mono={Boolean(user.email)} />
      <Fact
        label="Signs in through"
        value={
          user.auth_source === 'local'
            ? 'A password held here'
            : review.provider
              ? review.provider
              : // Federated to a provider that has since been deleted. The
                // account is still exactly as federated as it was, so saying
                // "local" here would be a lie.
                'An identity provider that is no longer configured'
        }
      />
      <Fact
        label="Last sign-in"
        value={user.last_login_at ? relativeAge(user.last_login_at) : 'Never'}
        title={user.last_login_at ? formatInstant(user.last_login_at) : undefined}
      />
      <Fact
        label="From"
        value={
          user.last_login_addr ||
          (user.last_login_at
            ? // Recorded from this release onwards; a sign-in that already
              // happened has no address left to go and find.
              'Not recorded for that sign-in'
            : '—')
        }
        mono={Boolean(user.last_login_addr)}
      />

      <div className="sm:col-span-2 xl:col-span-4">
        <p className="label text-faint">Groups</p>
        {review.groups.length === 0 ? (
          <p className="mt-1.5 text-[13px] text-muted">In no groups.</p>
        ) : (
          <div className="mt-1.5 flex flex-wrap gap-2">
            {review.groups.map((group) => (
              <Link
                key={group.id}
                to="/admin/groups"
                className="inline-flex items-center gap-1.5 rounded-chip border border-line bg-raised px-2 py-1 text-[12.5px] text-muted transition-colors hover:border-faint/60 hover:text-fg"
              >
                <UsersRound aria-hidden="true" className="size-3.5 shrink-0" />
                {group.name}
                {/* A derived membership is withdrawn on the next sign-in that no
                    longer carries the group, so how long it lasts is not this
                    installation's decision to make. */}
                {group.source === 'sso' ? (
                  <span className="text-[11px] text-faint">from the directory</span>
                ) : null}
              </Link>
            ))}
          </div>
        )}
      </div>

      {/* Said rather than left to be inferred, on the same principle the posture
          page states its non-goals: a review has to know what was not checked. */}
      <p className="text-[12px] text-muted sm:col-span-2 xl:col-span-4">
        kubemg records no second factor. For a federated account that is the identity provider's to
        assert and this console never sees it; for a local account there is none to report.
      </p>
    </Panel>
  )
}

/**
 * What they can reach, and why.
 *
 * The effective role is what the proxy will allow. The rows beneath it are the
 * grants that produced it — which is the half a review needs, because an
 * effective `cluster-admin` on production reads very differently depending on
 * whether somebody wrote it in 2024, a directory asserts it, or an approved
 * elevation ends in forty minutes.
 */
function Reach({ review }: { review: UserAccessReview }) {
  return (
    <Panel
      eyebrow="Access"
      title={
        review.clusters.length === 0
          ? 'Reaches no cluster'
          : `Reaches ${review.clusters.length} ${review.clusters.length === 1 ? 'cluster' : 'clusters'}`
      }
      description="The effective grant per cluster, as the gateway resolves it, with every grant that contributed to it."
      actions={
        <Link
          to="/admin/permissions"
          className="text-[12.5px] text-accent transition-colors hover:underline"
        >
          Edit in the matrix
        </Link>
      }
    >
      {review.clusters.length === 0 ? (
        <EmptyState icon={<KeyRound aria-hidden="true" className="size-5" />} title="No access">
          This account holds no grant on any registered cluster, directly or through a group.
        </EmptyState>
      ) : (
        <ul className="divide-y divide-line-soft">
          {review.clusters.map((entry) => (
            <li key={entry.cluster_id} className="flex flex-col gap-2 px-4 py-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-[11px] font-semibold text-faint">
                  {railChip({ name: entry.cluster, short_name: entry.short_name })}
                </span>
                <Link
                  to={`/clusters/${entry.cluster_id}`}
                  className="font-mono text-[13.5px] text-fg hover:underline"
                >
                  {entry.cluster}
                </Link>
                <EnvironmentTag environment={entry.environment} />
                <Pill tone={entry.k8s_role === 'cluster-admin' ? 'warn' : 'idle'}>
                  {entry.k8s_role}
                </Pill>
                <span className="text-[12.5px] text-muted">
                  {entry.namespaces.length === 0
                    ? 'every namespace'
                    : `${entry.namespaces.length} namespace${entry.namespaces.length === 1 ? '' : 's'}: ${entry.namespaces.join(', ')}`}
                </span>
                {entry.expires_at ? (
                  <Pill tone="warn">
                    <Timer aria-hidden="true" className="size-3" />
                    until {relativeAge(entry.expires_at)}
                  </Pill>
                ) : null}
              </div>

              <ul className="flex flex-col gap-1 pl-1">
                {entry.grants.map((grant, i) => (
                  <li
                    key={`${grant.origin}-${grant.group_id ?? grant.source ?? ''}-${i}`}
                    className="flex flex-wrap items-baseline gap-x-2 text-[12.5px] text-muted"
                  >
                    <span aria-hidden="true" className="text-faint">
                      ↳
                    </span>
                    <span className="font-medium text-fg">{grant.k8s_role}</span>
                    <span>{grantOrigin(grant.origin, grant.source, grant.group)}</span>
                    <span className="text-faint">
                      {grant.namespaces.length === 0
                        ? 'cluster-wide'
                        : grant.namespaces.join(', ')}
                    </span>
                    {grant.expires_at ? (
                      <span className="text-warn">ends {relativeAge(grant.expires_at)}</span>
                    ) : null}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

/** How one contributing grant reads in a sentence. The three direct sources are
    three different facts about how the access came to exist, and flattening them
    to "granted" is exactly what makes a matrix unreviewable. */
function grantOrigin(origin: string, source?: string, group?: string): string {
  if (origin === 'group') return `inherited from ${group ?? 'a group'}`
  switch (source) {
    case 'jit':
      return 'from an approved elevation'
    case 'sso':
      return 'asserted by the directory'
    default:
      return 'granted directly'
  }
}

/**
 * What they hold.
 *
 * A kubeconfig is the part of an access review that outlives everything else on
 * this page: it is a file on somebody's laptop, and revoking a grant does not
 * take it back. That is why it is a section here and not a link elsewhere.
 */
function Credentials({ rows, live }: { rows: IssuedKubeconfig[]; live: number }) {
  return (
    <Panel
      eyebrow="Credentials"
      title={live === 0 ? 'No live kubeconfig' : `${live} live kubeconfig${live === 1 ? '' : 's'}`}
      description="A kubeconfig is a file somebody already has. Removing a grant does not take one back — revoking it does."
      actions={
        <Link
          to="/admin/credentials"
          className="text-[12.5px] text-accent transition-colors hover:underline"
        >
          Open the register
        </Link>
      }
    >
      {rows.length === 0 ? (
        <EmptyState icon={<FileKey aria-hidden="true" className="size-5" />} title="None issued">
          This account has never generated a kubeconfig through kubemg.
        </EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th className="w-[28%]">Cluster</Th>
              <Th className="w-[16%]">Scope</Th>
              <Th className="w-[18%]">Status</Th>
              <Th className="w-[19%]">Expires</Th>
              <Th className="w-[19%]">Last used</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <Row key={row.id}>
                <Td className="truncate font-mono text-fg">{row.cluster_name}</Td>
                <Td className="text-[12.5px] text-muted">
                  {row.k8s_role ?? '—'}
                  {row.namespace ? ` · ${row.namespace}` : ''}
                </Td>
                <Td>
                  <Pill tone={row.status === 'active' ? 'ok' : 'idle'}>{row.status}</Pill>
                </Td>
                <Td className="text-[12.5px] text-muted" title={formatInstant(row.expires_at)}>
                  {relativeAge(row.expires_at)}
                </Td>
                <Td className="text-[12.5px] text-muted">
                  {/* Never used is a different fact from used long ago, and the
                      one an auditor asks about first. */}
                  {row.last_used_at ? relativeAge(row.last_used_at) : 'Never used'}
                </Td>
              </Row>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  )
}

/** What they did: the most recent interactive sessions, which is where a review
    stops being about policy and starts being about behaviour. */
function Sessions({ rows }: { rows: TerminalSession[] }) {
  return (
    <Panel
      eyebrow="Activity"
      title="Recent sessions"
      description="The last ten interactive sessions this account opened. The full trail, and any recording, is in Activity."
      actions={
        <Link
          to="/admin/recordings"
          className="text-[12.5px] text-accent transition-colors hover:underline"
        >
          Open session recordings
        </Link>
      }
    >
      {rows.length === 0 ? (
        <EmptyState icon={<Activity aria-hidden="true" className="size-5" />} title="No sessions">
          This account has not opened a terminal through kubemg.
        </EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th className="w-[24%]">Cluster</Th>
              <Th className="w-[36%]">Target</Th>
              <Th className="w-[20%]">Started</Th>
              <Th className="w-[20%]">Ended</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <Row key={row.id}>
                <Td className="truncate font-mono text-fg">{row.cluster}</Td>
                <Td className="truncate font-mono text-[12.5px] text-muted">
                  {row.namespace ? `${row.namespace}/` : ''}
                  {row.pod_name ?? '—'}
                  {row.container_name ? ` · ${row.container_name}` : ''}
                </Td>
                <Td className="text-[12.5px] text-muted" title={formatInstant(row.started_at)}>
                  {relativeAge(row.started_at)}
                </Td>
                <Td className="text-[12.5px] text-muted">
                  {row.ended_at ? relativeAge(row.ended_at) : <Pill tone="ok">open</Pill>}
                </Td>
              </Row>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  )
}

/** One labelled fact in the identity card. */
function Fact({
  label,
  value,
  mono,
  title,
}: {
  label: string
  value: string
  mono?: boolean
  title?: string
}) {
  return (
    <div className="min-w-0">
      <p className="label text-faint">{label}</p>
      <p
        title={title}
        className={`mt-1 min-w-0 truncate text-[13px] text-fg ${mono ? 'font-mono text-[12.5px]' : ''}`}
      >
        {value}
      </p>
    </div>
  )
}
