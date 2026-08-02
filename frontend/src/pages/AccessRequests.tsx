import { useSearchParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { JitApprovalsPanel } from '../components/jit/JitApprovalsPanel'
import { useClusters } from '../state/clusters-context'

/**
 * Access requests: the page behind the just-in-time workflow.
 *
 * It sits in the Access section next to the permissions matrix rather than inside
 * Settings, because it is not configuration — the matrix says what access exists
 * standing, and this says what access exists *right now* and who is waiting for
 * some. Those are two readings of the same subject and they belong side by side.
 *
 * Unlike the rest of that section it is **not** admin-only. The people who need to
 * ask are by definition the people without standing access, and the person whose
 * elevation is about to expire is the one who most needs to see it counting down.
 * The server narrows a non-admin to their own requests, exactly as it does on the
 * audit trail, and the panel says so rather than showing an ambiguous empty list.
 *
 * `?request=<id>` is what a chat notification links to, so the page can be opened
 * on the thing the message was about.
 */
export function AccessRequests() {
  const { clusters } = useClusters()
  const [params] = useSearchParams()

  return (
    <AppShell title="Access requests">
      <JitApprovalsPanel clusters={clusters} focusRequest={params.get('request')} />
    </AppShell>
  )
}
