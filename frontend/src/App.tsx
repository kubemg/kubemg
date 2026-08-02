import type { ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AccessRequests } from './pages/AccessRequests'
import { AuditTrail } from './pages/AuditTrail'
import { AuthCallback } from './pages/AuthCallback'
import { ClusterDetail } from './pages/ClusterDetail'
import { ClusterManagement } from './pages/ClusterManagement'
import { ClusterWizard } from './pages/ClusterWizard'
import { Explore } from './pages/Explore'
import { GroupManagement } from './pages/GroupManagement'
import { Login } from './pages/Login'
import { Overview } from './pages/Overview'
import { PermissionsMatrix } from './pages/PermissionsMatrix'
import { SessionRecordings } from './pages/SessionRecordings'
import { AgentSettings } from './pages/settings/AgentSettings'
import { AlertingSettings } from './pages/settings/AlertingSettings'
import { AuditSettings } from './pages/settings/AuditSettings'
import { GeneralSettings } from './pages/settings/GeneralSettings'
import { GuardrailsSettings } from './pages/settings/GuardrailsSettings'
import { SsoSettings } from './pages/settings/SsoSettings'
import { UserManagement } from './pages/UserManagement'
import { AuthProvider } from './state/AuthProvider'
import { ClustersProvider } from './state/ClustersProvider'
import { useAuth } from './state/auth-context'

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

/** RequireAuth gates the app; adminOnly additionally gates the admin pages. */
function RequireAuth({ children, adminOnly = false }: { children: ReactNode; adminOnly?: boolean }) {
  const { user, loading } = useAuth()

  if (loading) return <RestoringSession />
  if (!user) return <Navigate to="/login" replace />
  if (adminOnly && user.role !== 'admin') return <Navigate to="/" replace />

  return <ClustersProvider>{children}</ClustersProvider>
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

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<LoginRoute />} />
          {/* Where an interactive SSO sign-in comes back to. It is outside
              RequireAuth because it is what creates the session; once it has,
              the redirect below sends the browser on. */}
          <Route path="/auth/callback" element={<CallbackRoute />} />
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
          <Route
            path="/clusters/:id"
            element={
              <RequireAuth>
                <ClusterDetail />
              </RequireAuth>
            }
          />
          {/* Which cluster is being explored is part of the address, not page
              state: the rail's cluster list is how an operator switches, and a
              link to what someone is looking at has to carry the cluster. */}
          <Route
            path="/explore"
            element={
              <RequireAuth>
                <Explore />
              </RequireAuth>
            }
          />
          <Route
            path="/explore/:clusterId"
            element={
              <RequireAuth>
                <Explore />
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
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  )
}
