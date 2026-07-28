import type { ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
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
import { Settings } from './pages/Settings'
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
          <Route
            path="/explore"
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
          <Route
            path="/settings"
            element={
              <RequireAuth adminOnly>
                <Settings />
              </RequireAuth>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  )
}
