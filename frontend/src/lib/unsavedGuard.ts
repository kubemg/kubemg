import { useEffect, useRef } from 'react'
import { useNavigate } from 'react-router'
import { useConfirm } from '../state/confirm-context'

/*
 * Holding on to an edit that has not been saved.
 *
 * Two ways out of a page can throw an edit away. Leaving the console — a
 * reload, a closed tab, a typed address — is the browser's to ask about, and
 * `beforeunload` is the only thing that reaches it. Following a link inside
 * the console is ours: the console runs on a plain router with no navigation
 * blocker, so a click on a same-origin link is caught on its way down, the
 * question is asked on the console's own confirmation dialog, and the
 * navigation is replayed if the answer is to leave.
 *
 * A modified click (a new tab, a new window) leaves the edit where it is, so
 * it is not asked about.
 */
export function useUnsavedGuard(dirty: boolean, what = 'your changes') {
  const confirm = useConfirm()
  const navigate = useNavigate()
  const asking = useRef(false)

  useEffect(() => {
    if (!dirty) return

    function onBeforeUnload(event: BeforeUnloadEvent) {
      event.preventDefault()
      // Older engines only show the prompt when a value is set.
      event.returnValue = ''
    }

    function onClick(event: MouseEvent) {
      if (event.defaultPrevented || event.button !== 0) return
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
      const anchor = (event.target as Element | null)?.closest?.('a[href]')
      if (!(anchor instanceof HTMLAnchorElement)) return
      if (anchor.target && anchor.target !== '_self') return
      const url = new URL(anchor.href, window.location.href)
      if (url.origin !== window.location.origin) return
      if (url.pathname === window.location.pathname && url.search === window.location.search) return

      event.preventDefault()
      event.stopPropagation()
      if (asking.current) return
      asking.current = true
      void confirm({
        title: 'Leave without saving?',
        body: `You have not saved ${what}. Leaving this page throws them away.`,
        confirmLabel: 'Leave',
        tone: 'danger',
      }).then((leave) => {
        asking.current = false
        if (leave) navigate(`${url.pathname}${url.search}${url.hash}`)
      })
    }

    window.addEventListener('beforeunload', onBeforeUnload)
    document.addEventListener('click', onClick, true)
    return () => {
      window.removeEventListener('beforeunload', onBeforeUnload)
      document.removeEventListener('click', onClick, true)
    }
  }, [dirty, what, confirm, navigate])
}
