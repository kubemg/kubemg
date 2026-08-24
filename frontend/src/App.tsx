import type { ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation, useParams } from 'react-router'
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
import type { ClusterPage } from './lib/navigation'
import { DEFAULT_RESOURCE } from './lib/navigation'
import { AuthProvider } from './state/AuthProvider'
import { ClustersProvider } from './state/ClustersProvider'
import { InventoryProvider } from './state/InventoryProvider'
import { TimeRangeProvider } from './state/TimeRangeProvider'
import { Lockup } from './components/Mark'
import { useAuth } from './state/auth-context'
import { useClusters } from './state/clusters-context'

function RestoringSession() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 bg-bg">
      <Lockup className="text-[18px] text-fg" />
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

  return (
    <ClustersProvider>
      {/* The tree is drawn on every one of a cluster's pages, so what a cluster
          can browse is read here rather than by whichever page happens to be
          open. */}
      <InventoryProvider>{children}</InventoryProvider>
    </ClustersProvider>
  )
}

/** What somebody without the rights to finish setup sees while it is unfinished. */
function SetupPending() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 bg-bg px-6 text-center">
      <Lockup className="text-[18px] text-fg" />
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
  return <Navigate to={`/clusters/${clusterId}/${DEFAULT_RESOURCE}`} replace />
}

/**
 * `/clusters/:id/explore/...` — the address every resource list had while the
 * tree lived inside a page called Explore. The word is gone from the interface,
 * so it is gone from the address; the kind sits directly under the cluster now.
 * The query string comes along because it carries the namespace, which is the
 * half of one of these links that people actually care about.
 */
function ExploreResourceRedirect() {
  const params = useParams<{ id: string; '*': string }>()
  const { search } = useLocation()
  const key = params['*'] || DEFAULT_RESOURCE
  return <Navigate to={`/clusters/${params.id}/${key}${search}`} replace />
}

/** A cluster page whose slug changed, at the address it changed from. */
function ClusterPageRedirect({ page }: { page: ClusterPage }) {
  const { id } = useParams<{ id: string }>()
  const { search } = useLocation()
  return <Navigate to={`/clusters/${id}/${page}${search}`} replace />
}

/** A bookmarked Settings tab, at the address that tab used to have. */
function SettingsPaneRedirect() {
  const { pane } = useParams<{ pane: string }>()
  return <Navigate to={`/admin/settings/${pane ?? 'general'}`} replace />
}

/**
 * An address that moved wholesale into `/admin` or `/me`. Registered so that
 * every link already pasted into a runbook still lands, and so a bookmark on
 * the old Settings tab does not answer with the fleet overview.
 */
function MovedTo({ to }: { to: string }) {
  const { search } = useLocation()
  return <Navigate to={`${to}${search}`} replace />
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
    return <Navigate to={`/clusters/${reachable.id}/${DEFAULT_RESOURCE}`} replace />
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

            {/* ── A cluster's own space ──────────────────────────────────────
                Which cluster, which kind, and which namespace are all in the
                address — "the Services in `payments` on prod-eu-west-1" is a
                link, not a sequence of clicks to reproduce.

                The five page slugs below are reserved: the resource route is a
                splat, so a kind that collided with one of them would be
                unreachable. Everything else under a cluster is a resource key,
                and it is a splat rather than a `:kind` segment because a
                discovered CRD's key (`crd:group/version/plural`) carries
                slashes of its own that one segment cannot hold. */}

            {/* Registered before /clusters/:id so "new" is never read as an id. */}
            <Route
              path="/clusters/new"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/clusters/new" />
                </RequireAuth>
              }
            />
            {/* The inventory is administration; `/` is the picker. */}
            <Route
              path="/clusters"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/clusters" />
                </RequireAuth>
              }
            />
            <Route
              path="/clusters/:id"
              element={
                <RequireAuth>
                  <Navigate to="dashboard" replace />
                </RequireAuth>
              }
            />
            {/* Summary became Dashboard: it is the first row under Cluster in
                the tree, and "dashboard" is what everybody arriving from
                Rancher or Lens already calls it. */}
            <Route
              path="/clusters/:id/summary"
              element={
                <RequireAuth>
                  <ClusterPageRedirect page="dashboard" />
                </RequireAuth>
              }
            />
            <Route
              path="/clusters/:id/dashboard"
              element={
                <RequireAuth>
                  <ClusterSummary />
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
                than a tab on the dashboard because it answers a question the
                dashboard's Capacity panel cannot — what the scheduler has
                already promised away — and because "why will nothing schedule"
                is a question somebody arrives with, and arriving means a link. */}
            <Route
              path="/clusters/:id/capacity"
              element={
                <RequireAuth>
                  <NodeCapacity />
                </RequireAuth>
              }
            />
            {/* Workload security posture: seven fixed rules over fields the
                resource lists already read, per cluster or per namespace. */}
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
            {/* The tree used to live inside a page called Explore, one click
                below the cluster. Both of its addresses now redirect onto the
                kind itself. */}
            <Route
              path="/clusters/:id/explore"
              element={
                <RequireAuth>
                  <ExploreResourceRedirect />
                </RequireAuth>
              }
            />
            <Route
              path="/clusters/:id/explore/*"
              element={
                <RequireAuth>
                  <ExploreResourceRedirect />
                </RequireAuth>
              }
            />
            {/* Every other address under a cluster is one of its resource
                lists. Ranked below every static route above it, so it only
                catches what none of them claimed. */}
            <Route
              path="/clusters/:id/*"
              element={
                <RequireAuth>
                  <Explore />
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

            {/* ── The operator's own business ────────────────────────────────
                Not adminOnly: the people who need to ask for access are the
                ones without it, and the server narrows a non-admin to their own
                requests exactly as it does on the audit trail. */}
            <Route
              path="/me/access"
              element={
                <RequireAuth>
                  <AccessRequests />
                </RequireAuth>
              }
            />
            <Route
              path="/access-requests"
              element={
                <RequireAuth>
                  <MovedTo to="/me/access" />
                </RequireAuth>
              }
            />

            {/* ── Administration ────────────────────────────────────────────
                One door, admins only. Registering a cluster, mapping identity,
                writing guardrails and reading the fleet-wide trail are the
                platform team's work; they used to sit one row below the thing a
                developer opens forty times a day. */}
            <Route path="/admin" element={<Navigate to="/admin/clusters" replace />} />
            {/* Before /admin/clusters so "new" is never read as an id. */}
            <Route
              path="/admin/clusters/new"
              element={
                <RequireAuth adminOnly>
                  <ClusterWizard />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/clusters"
              element={
                <RequireAuth adminOnly>
                  <ClusterManagement />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/users"
              element={
                <RequireAuth adminOnly>
                  <UserManagement />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/groups"
              element={
                <RequireAuth adminOnly>
                  <GroupManagement />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/permissions"
              element={
                <RequireAuth adminOnly>
                  <PermissionsMatrix />
                </RequireAuth>
              }
            />
            {/* The approver's door onto the same page `/me/access` is the
                asker's. Two entrances, one surface, narrowed by the server. */}
            <Route
              path="/admin/access-requests"
              element={
                <RequireAuth adminOnly>
                  <AccessRequests />
                </RequireAuth>
              }
            />
            {/* The fleet-wide trail. A cluster's own trail stays in its tree. */}
            <Route
              path="/admin/audit"
              element={
                <RequireAuth adminOnly>
                  <AuditTrail />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/recordings"
              element={
                <RequireAuth adminOnly>
                  <SessionRecordings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings"
              element={<Navigate to="/admin/settings/general" replace />}
            />
            <Route
              path="/admin/settings/general"
              element={
                <RequireAuth adminOnly>
                  <GeneralSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings/agent"
              element={
                <RequireAuth adminOnly>
                  <AgentSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings/audit"
              element={
                <RequireAuth adminOnly>
                  <AuditSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings/guardrails"
              element={
                <RequireAuth adminOnly>
                  <GuardrailsSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings/alerting"
              element={
                <RequireAuth adminOnly>
                  <AlertingSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings/sso"
              element={
                <RequireAuth adminOnly>
                  <SsoSettings />
                </RequireAuth>
              }
            />
            <Route
              path="/admin/settings/deployment"
              element={
                <RequireAuth adminOnly>
                  <DeploymentSettings />
                </RequireAuth>
              }
            />

            {/* ── Where administration used to live ─────────────────────────
                Every one of these is in somebody's bookmarks or a runbook. */}
            <Route
              path="/users"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/users" />
                </RequireAuth>
              }
            />
            <Route
              path="/groups"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/groups" />
                </RequireAuth>
              }
            />
            <Route
              path="/permissions"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/permissions" />
                </RequireAuth>
              }
            />
            <Route
              path="/audit"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/audit" />
                </RequireAuth>
              }
            />
            <Route
              path="/recordings"
              element={
                <RequireAuth adminOnly>
                  <MovedTo to="/admin/recordings" />
                </RequireAuth>
              }
            />
            <Route path="/settings" element={<Navigate to="/admin/settings/general" replace />} />
            <Route
              path="/settings/:pane"
              element={
                <RequireAuth adminOnly>
                  <SettingsPaneRedirect />
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
