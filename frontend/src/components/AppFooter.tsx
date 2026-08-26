import { useEffect, useState } from 'react'
import { ExternalLink } from 'lucide-react'

import { fetchServerVersion } from '../api/client'
import type { ServerVersion } from '../api/types'

/**
 * The one line under every page: which release this is, and where the manual
 * for it lives.
 *
 * Both facts come from the server rather than from the console's own build. In
 * a real install the console *is* the server — the binary embeds it — and the
 * version is what somebody has to have to hand before reading a changelog or
 * opening an issue, so it must be the running process's own number and not
 * whatever was stamped into a bundle at some other time.
 *
 * It draws nothing until the read answers, and nothing at all if it fails: a
 * footer is the quietest thing on the page and an error in it would be the
 * loudest. The version is mono because it is data, like every other version in
 * this console.
 */
export function AppFooter() {
  const [info, setInfo] = useState<ServerVersion | null>(null)

  useEffect(() => {
    let live = true
    fetchServerVersion()
      .then((answer) => {
        if (live) setInfo(answer)
      })
      .catch(() => {
        /* Nothing to say. See above. */
      })
    return () => {
      live = false
    }
  }, [])

  if (!info) return null

  return (
    <footer className="mt-2 border-t border-line px-4 py-3 xl:px-6">
      <div className="mx-auto flex max-w-[1440px] flex-wrap items-center gap-x-4 gap-y-1">
        <span className="font-mono text-[11.5px] text-faint">
          kubemg <span className="text-muted">{info.version}</span>
        </span>
        <a
          href={info.docs_url}
          target="_blank"
          rel="noreferrer noopener"
          className="ml-auto inline-flex items-center gap-1.5 text-[12px] text-muted transition-colors hover:text-fg"
        >
          Documentation
          <ExternalLink aria-hidden="true" className="size-3.5" />
        </a>
      </div>
    </footer>
  )
}
