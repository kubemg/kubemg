/*
 * The fleet page — one route, two bodies.
 *
 * This page used to ask the same question of everybody who signed in, and the
 * two people who arrive at it do not have the same question. Reachable, Tunnels
 * open and Last sweep are an *administrator's* facts; a developer lands here to
 * find out which clusters they can open and with what. So the route splits the
 * way `/clusters/:id/dashboard` already splits, off the same coarse
 * `user.role === 'admin'` — a super admin is an administrator here.
 *
 * The shell is what both bodies share, because it is what either of them came
 * for: the title, the live chip, the errors. Only *Run check* is admin-only —
 * a developer cannot probe a cluster's health and is not offered it; their
 * header action is the one thing they might actually want from a fleet they
 * cannot fully reach, which is to ask for access to more of it.
 *
 * See `components/FleetOperatorBody.tsx` and `components/FleetDeveloperBody.tsx`
 * for what each one draws and why.
 */

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { Plus, RefreshCw, Server } from 'lucide-react'
import { checkCluster, errorMessage, fetchJitRequests } from '../api/client'
import type { JitRequest } from '../api/types'
import { AppShell } from '../components/AppShell'
import { FleetDeveloperBody } from '../components/FleetDeveloperBody'
import { FleetOperatorBody } from '../components/FleetOperatorBody'
import { JitRequestModal } from '../components/jit/JitRequestModal'
import { LiveChip } from '../components/LiveRefresh'
import { Button, EmptyState, Notice } from '../components/primitives'
import { FLEET_INTERVAL } from '../lib/live'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

export function Overview() {
  const { clusters, loading, error, reload } = useClusters()
  const { user } = useAuth()
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [requesting, setRequesting] = useState(false)

  const isAdmin = user?.role === 'admin'
  const { pending, elevation, reloadRequests } = useAccessRequests(isAdmin)

  async function checkAll() {
    setChecking(true)
    setCheckError(null)
    try {
      const results = await Promise.allSettled(clusters.map((cluster) => checkCluster(cluster.id)))
      const failed = results.find((result) => result.status === 'rejected')
      if (failed && failed.status === 'rejected') {
        setCheckError(errorMessage(failed.reason, 'Some clusters could not be checked.'))
      }
      await reload()
    } finally {
      setChecking(false)
    }
  }

  return (
    <AppShell
      title="Fleet"
      actions={
        <div className="flex items-center gap-2">
          {/* The fleet list re-reads itself, which is how a cluster registered a
              minute ago appears here the moment its agent dials in. The chip is
              where that is said, and where it is turned off. */}
          <LiveChip interval={FLEET_INTERVAL} />
          {isAdmin ? (
            clusters.length > 0 ? (
              <Button variant="primary" onClick={checkAll} disabled={checking}>
                <RefreshCw
                  aria-hidden="true"
                  className={`size-4 ${checking ? 'animate-spin' : ''}`}
                />
                {checking ? 'Checking…' : 'Check every cluster'}
              </Button>
            ) : null
          ) : null}
        </div>
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {checkError ? <Notice tone="error">{checkError}</Notice> : null}

        {loading && clusters.length === 0 ? (
          <p className="text-[13px] text-muted">Loading the fleet…</p>
        ) : null}

        {!loading && clusters.length === 0 ? (
          <div className="card">
            <EmptyState
              icon={<Server aria-hidden="true" className="size-5" />}
              title={isAdmin ? 'No clusters yet' : 'No clusters granted to you'}
              action={
                isAdmin ? (
                  <Link to="/admin/clusters/new">
                    <Button variant="primary">
                      <Plus aria-hidden="true" className="size-4" />
                      Register a cluster
                    </Button>
                  </Link>
                ) : (
                  <Button variant="primary" onClick={() => setRequesting(true)}>
                    Request access
                  </Button>
                )
              }
            >
              {isAdmin
                ? 'Register one to bring it under kubemg. The cluster dials out to here, so nothing needs to be opened inbound.'
                : 'Ask for access to a cluster, or an administrator can grant you one.'}
            </EmptyState>
          </div>
        ) : null}

        {clusters.length > 0 ? (
          isAdmin ? (
            <FleetOperatorBody clusters={clusters} pendingRequests={pending} />
          ) : (
            <FleetDeveloperBody
              clusters={clusters}
              elevation={elevation}
              onRequestAccess={() => setRequesting(true)}
            />
          )
        ) : null}
      </div>

      {requesting ? (
        <JitRequestModal
          clusters={clusters}
          onClose={() => setRequesting(false)}
          onCreated={() => {
            setRequesting(false)
            void reloadRequests()
          }}
        />
      ) : null}
    </AppShell>
  )
}

/**
 * The one thing on this page that is neither the fleet nor its capacity: what
 * is waiting in the access queue.
 *
 * Both bodies want the same list for opposite reasons — an administrator wants
 * the count of what is waiting on *them*, a developer wants the elevation
 * that is running for *them* — and the server already narrows a non-admin to
 * their own requests, so the same call answers both. It is read once here
 * rather than in either body, so the two never both ask.
 *
 * A failure is swallowed on purpose. This is a secondary reading on a page
 * whose subject is the fleet: an approvals endpoint that is briefly unavailable
 * must cost the caller one queue row, not the cluster list they came for.
 */
function useAccessRequests(isAdmin: boolean): {
  pending: number
  elevation: JitRequest | null
  reloadRequests: () => Promise<void>
} {
  const [pending, setPending] = useState(0)
  const [elevation, setElevation] = useState<JitRequest | null>(null)

  const reloadRequests = useCallback(async () => {
    try {
      const list = await fetchJitRequests()
      setPending(list.pending)
      // The caller's own live elevation. An admin's queue count comes from the
      // server's own `pending`, which is already scoped to what they may decide.
      setElevation(list.requests.find((request) => request.active) ?? null)
    } catch {
      setPending(0)
      setElevation(null)
    }
  }, [])

  useEffect(() => {
    void reloadRequests()
    // Re-read when the role the page is drawn for changes, which is a sign-in
    // rather than anything this page does. It is deliberately not on the live
    // tick: an approval queue is checked by opening it, and a count that moves
    // under somebody mid-read is worse than one that is a minute old.
  }, [isAdmin, reloadRequests])

  return { pending, elevation, reloadRequests }
}
