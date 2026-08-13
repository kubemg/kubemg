import type { ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useParams } from 'react-router'
import { AccessRequests } from './pages/AccessRequests'
import { AuditTrail } from './pages/AuditTrail'
import { AuthCallback } from './pages/AuthCallback'
import { ClusterManagement } from './pages/ClusterManagement'
import { ClusterSummary } from './pages/ClusterSummary'
import { ClusterWizard } from './pages/ClusterWizard'
import { EventsTimeline } from './pages/EventsTimeline'
import { Explore } from './pages/Explore'
import { GroupManagement } from './pages/GroupManagement'
import { Login } from './pages/Login'
import { NodeCapacity } from './pages/NodeCapacity'
import { ClusterCost } from './pages/ClusterCost'
import { CostSettings } from './pages/settings/CostSettings'
import { Overview } from './pages/Overview'
import { PermissionsMatrix } from './pages/PermissionsMatrix'
import { SecurityPosture } from './pages/SecurityPosture'
import { SessionRecordings } from './pages/SessionRecordings'
import { Setup } from './pages/Setup'
import { AgentSettings } from './pages/settings/AgentSettings'
import { AlertingSettings } from './pages/settings/AlertingSettings'
import { AuditSettings } from './pages/settings/AuditSettings'
import { DeploymentSettings } from './pages/settings/DeploymentSettings'
import { GeneralSettings } from './pages/settings/GeneralSettings'
import { GuardrailsSettings } from './pages/settings/GuardrailsSettings'
import { SsoSettings } from './pages/settings/SsoSettings'
import { UserManagement } from './pages/UserManagement'
import { AuthProvider } from './state/AuthProvider'
import { ClustersProvider } from './state/ClustersProvider'
import { TimeRangeProvider } from './state/TimeRangeProvider'
import { useAuth } from './state/auth-context'
import { useClusters } from './state/clusters-context'

function RestoringSession() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 bg-bg">
      <span className="text-[18px] font-semibold tracking-[-0.02em]">
        <span className="text-fg">Kube</span>
        <span className="text-accent">MG</span>
      </span>
      <p className="label">Restoring session…</p>
    </div>
  )
}

/**
 * RequireAuth gates the app; adminOnly additionally gates the admin pages.
 *
 * It also holds the setup gate. A server that has never been configured has no
 * address clusters can reach and no administrator password worth the name, so
 * an admin is sent to the wizard rather than to a console whose every page would
 * be about a fleet that cannot exist yet. A non-admin is shown why instead:
 * there is nothing for them to do about it, and a redirect loop into a page they
 * may not open would be worse than an explanation.
 */
function RequireAuth({ children, adminOnly = false }: { children: ReactNode; adminOnly?: boolean }) {
  const { user, loading, setupRequired, setupLoading } = useAuth()

  if (loading || setupLoading) return <RestoringSession />
  if (!user) return <Navigate to="/login" replace />
  if (setupRequired) {
    return user.role === 'admin' ? <Navigate to="/setup" replace /> : <SetupPending />
  }
  if (adminOnly && user.role !== 'admin') return <Navigate to="/" replace />

  return <ClustersProvider>{children}</ClustersProvider>
}

/** What somebody without the rights to finish setup sees while it is unfinished. */
function SetupPending() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 bg-bg px-6 text-center">
      <span className="text-[18px] font-semibold tracking-[-0.02em]">
        <span className="text-fg">Kube</span>
        <span className="text-accent">MG</span>
      </span>
      <p className="max-w-sm text-[13px] leading-relaxed text-muted">
        This bastion has not finished being set up. An administrator has to complete the install
        before there is anything here to reach.
      </p>
    </div>
  )
}

/**
 * The wizard's own route. It runs once: the moment the install is stamped, this
 * address stops being a page and becomes a redirect, and every field it held
 * lives on its own Settings page instead.
 */
function SetupRoute() {
  const { user, loading, setupRequired, setupLoading } = useAuth()

  if (loading || setupLoading) return <RestoringSession />
  if (!user) return <Navigate to="/login" replace />
  if (!setupRequired || user.role !== 'admin') return <Navigate to="/" replace />

  return <Setup />
}

function LoginRoute() {
  const { user, loading } = useAuth()

  if (loading) return <RestoringSession />
  return user ? <Navigate to="/" replace /> : <Login />
}

/** The SSO landing page, which becomes a redirect the moment it has a session. */
function CallbackRoute() {
  const { user } = useAuth()

  return user ? <Navigate to="/" replace /> : <AuthCallback />
}

/**
 * A pasted `/explore/:clusterId` link — the address people already have in
 * tickets and bookmarks. The id still names the same cluster; only the space
 * it lives under moved, onto its default resource.
 */
function ExploreClusterRedirect() {
  const { clusterId } = useParams<{ clusterId: string }>()
  return <Navigate to={`/clusters/${clusterId}/explore/pods`} replace />
}

/**
 * `/explore` with no cluster named settles on the first readable one, exactly
 * as it did when the id lived in this same path — only now that decision is
 * made once, at the route, rather than by an effect inside a page that always
 * addresses one cluster through `:id`. When nothing in the fleet is reachable
 * there is nowhere to send the redirect, and `Explore` still owns that
 * explanation (it is reached here with no id, the same shape it renders for).
 */
function ExploreLanding() {
  const { clusters, loading } = useClusters()
  const reachable = clusters.find(
    (cluster) => cluster.connection_mode === 'agent' && cluster.agent_attached,
  )
  if (!loading && reachable) {
    return <Navigate to={`/clusters/${reachable.id}/explore/pods`} replace />
  }
  return <Explore />
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        {/* The console's time range. It is inside the router because it lives
            in the address, and outside the routes because it is one window for
            the whole console rather than a property of any page. */}
        <TimeRangeProvider>
          <Routes>
            <Route path="/login" element={<LoginRoute />} />
            {/* Where an interactive SSO sign-in comes back to. It is outside
                RequireAuth because it is what creates the session; once it has,
                the redirect below sends the browser on. */}
            <Route path="/auth/callback" element={<CallbackRoute />} />
            {/* First-run setup. Outside RequireAuth because RequireAuth is what
                redirects *into* it — routing it through the same gate would be a
                loop. It does its own gating instead. */}
            <Route path="/setup" element={<SetupRoute />} />
            <Route
              path="/"
              element={
                <RequireAuth>
                  <Overview />
                </RequireAuth>
              }
            />
            <Route
              path="/clusters"
              element={
                <RequireAuth adminOnly>
                  <ClusterManagement />
                </RequireAuth>
              }
            />
            {/* Registered before /clusters/:id so "new" is never read as an id. */}
            <Route
              path="/clusters/new"
              element={
                <RequireAuth adminOnly>
                  <ClusterWizard />
                </RequireAuth>
              }
            />
            {/* A cluster now has an address space of its own — /clusters/:id is
                nothing on its own, just where its default view lives. The
                redirect is relative so it preserves whatever id matched. */}
            <Route
              path="/clusters/:id"
              element={
                <RequireAuth>
                  <Navigate to="summary" replace />
                </RequireAuth>
              }
            />
            <Route
              path="/clusters/:id/summary"
              element={
                <RequireAuth>
                  <ClusterSummary />
                </RequireAuth>
              }
            />
            {/* Which cluster is being explored — and which resource, and which
                namespace — is part of the address, not page state: the entity
                switcher, the fleet list and the palette are how an operator moves
                between clusters, and a link to what someone is looking at has to
                carry all three. The resource key is a splat rather than a plain
                `:kind` because a discovered CRD's key (`crd:group/version/plural`)
                contains slashes of its own. */}
            <Route
              path="/clusters/:id/explore"
              element={
                <RequireAuth>
                  <Navigate to="pods" replace />
                </RequireAuth>
              }
            />
            <Route
              path="/clusters/:id/explore/*"
              element={
                <RequireAuth>
                  <Explore />
                </RequireAuth>
              }
            />
            {/* What the cluster itself recorded, as opposed to what KubeMG did:
                the trail below is every call KubeMG made, and this is every
                event Kubernetes wrote. Cluster-scoped because events are, and
                narrowable to one object — which is what the pilot header's
                alerts link into. */}
            <Route
              path="/clusters/:id/events"
              element={
                <RequireAuth>
                  <EventsTimeline />
                </RequireAuth>
              }
            />
            {/* Allocation rather than consumption. It is its own address rather
                than a tab on the summary because it answers a question the
                summary's Capacity panel cannot — what the scheduler has already
                promised away — and because "why will nothing schedule" is a
                question somebody arrives with, and arriving means a link. */}
            <Route
              path="/clusters/:id/capacity"
              element={
                <RequireAuth>
                  <NodeCapacity />
                </RequireAuth>
              }
            />
            {/* Cost sits beside Capacity rather than inside it because it is a
                different question with a different audience: allocation is read
                by whoever is trying to get a pod scheduled, and the bill is read
                by whoever is being asked about it. They share the arithmetic and
                nothing else. */}
            <Route
              path="/clusters/:id/cost"
              element={
                <RequireAuth>
                  <ClusterCost />
                </RequireAuth>
              }
            />
            {/* Workload security posture: seven fixed rules over fields Explore
                already reads, per cluster or per namespace. Reached the same
                way Events is — a live tunnel, and a row in the cluster's own
                quick-nav — because it answers a question about the cluster as
                a whole rather than about one object somebody already opened. */}
            <Route
              path="/clusters/:id/security"
              element={
                <RequireAuth>
                  <SecurityPosture />
                </RequireAuth>
              }
            />
            {/* Not adminOnly: the server narrows a non-admin to their own rows on
                a cluster's trail exactly as it does on the fleet-wide one. */}
            <Route
              path="/clusters/:id/audit"
              element={
                <RequireAuth>
                  <AuditTrail />
                </RequireAuth>
              }
            />
            <Route
              path="/explore"
              element={
                <RequireAuth>
                  <ExploreLanding />
                </RequireAuth>
              }
            />
            {/* A link people have pasted into tickets under the old address. */}
            <Route
              path="/explore/:clusterId"
              element={
                <RequireAuth>
                  <ExploreClusterRedirect />
                </RequireAuth>
              }
            />
            <Route
              path="/audit"
              element={
                <RequireAuth>
                  <AuditTrail />
                </RequireAuth>
              }
            />
            {/* Everyone reaches their own recordings; an admin reaches the fleet's.
                The narrowing is the server's, exactly as it is for the trail. */}
            <Route
              path="/recordings"
              element={
                <RequireAuth>
                  <SessionRecordings />
                </RequireAuth>
              }
            />
            {/* Not adminOnly: the people who need to ask for access are the ones
                without it, and the server narrows a non-admin to their own
                requests exactly as it does on the audit trail. */}
            <Route
              path="/access-requests"
              element={
                <RequireAuth>
                  <AccessRequests />
                </RequireAuth>
              }
            />
            <Route
              path="/users"
              element={
                <RequireAuth adminOnly>
                  <UserManagement />
                </RequireAuth>
              }
            />
            <Route
              path="/groups"
              element={
                <RequireAuth adminOnly>
                  <GroupManagement />
                </RequireAuth>
              }
            />
            <Route
              path="/permissions"
              element={
                <RequireAuth adminOnly>
                  <PermissionsMatrix />
                </RequireAuth>
              }
            />
            <Route path="/settings" element={<Navigate to="/settings/general" replace />} />
            <Route
              path="/settings/general"
              element={
                <RequireAuth adminOnly>
                  <GeneralSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/settings/agent"
              element={
                <RequireAuth adminOnly>
                  <AgentSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/settings/audit"
              element={
                <RequireAuth adminOnly>
                  <AuditSettings />
                </RequireAuth>
              }
            />
            {/* The rates every cost figure is computed against. Administrative
                because it decides what the whole console says about money. */}
            <Route
              path="/settings/cost"
              element={
                <RequireAuth adminOnly>
                  <CostSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/settings/guardrails"
              element={
                <RequireAuth adminOnly>
                  <GuardrailsSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/settings/alerting"
              element={
                <RequireAuth adminOnly>
                  <AlertingSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/settings/sso"
              element={
                <RequireAuth adminOnly>
                  <SsoSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/settings/deployment"
              element={
                <RequireAuth adminOnly>
                  <DeploymentSettings />
                </RequireAuth>
              }
            />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </TimeRangeProvider>
      </AuthProvider>
    </BrowserRouter>
  )
}
