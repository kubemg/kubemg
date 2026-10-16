import { useEffect, useState } from 'react'
import { Download } from 'lucide-react'
import { fetchCLIDownloads } from '../api/client'
import type { CLIDownloads } from '../api/types'
import { CodeBlock } from './primitives'

const CLI_RELEASES_URL = 'https://github.com/kubemg/kubemg/releases'

const OS_WORD: Record<string, string> = { linux: 'Linux', darwin: 'macOS', windows: 'Windows' }

/** Which OS this browser is on, to put its downloads first. A guess, and only
    an ordering: every platform is always offered. */
function browserOS(): string {
  const agent = typeof navigator === 'undefined' ? '' : navigator.userAgent
  if (/Windows/i.test(agent)) return 'windows'
  if (/Mac/i.test(agent)) return 'darwin'
  return 'linux'
}

/**
 * The commands that give kubectl access through the kubemg CLI, with this
 * server's address and the cluster's name filled in — rendered from what the
 * server says rather than typed by whoever reads them, the agent install
 * sheet's reason: commands a person retypes are commands that drift.
 *
 * With `showInstall` it also says where the binary comes from: this server,
 * when its image carries the binaries (which is what an air-gapped site
 * relies on), else the releases page.
 */
export function CliCommands({
  serverURL,
  cluster,
  showInstall = false,
}: {
  serverURL: string
  cluster?: string
  showInstall?: boolean
}) {
  const server = serverURL || window.location.origin
  const [downloads, setDownloads] = useState<CLIDownloads | null>(null)

  useEffect(() => {
    if (!showInstall) return
    let live = true
    fetchCLIDownloads()
      .then((answer) => {
        if (live) setDownloads(answer)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [showInstall])

  const mine = browserOS()
  const offered = downloads?.available
    ? [...downloads.downloads].sort((a, b) => Number(b.os === mine) - Number(a.os === mine))
    : []
  const unix = offered.find((entry) => entry.os === mine && entry.os !== 'windows') ?? offered.find((entry) => entry.os === 'linux')

  return (
    <div className="flex flex-col gap-3">
      {showInstall && offered.length > 0 ? (
        <div className="flex flex-col gap-2">
          <span className="label">Download kubemg from this server</span>
          <div className="flex flex-wrap gap-2">
            {offered.map((entry) => (
              <a
                key={`${entry.os}/${entry.arch}`}
                href={entry.path}
                download
                title={`SHA-256 ${entry.sha256}`}
                className="inline-flex items-center gap-1.5 rounded-control border border-line bg-surface px-2.5 py-1.5 text-[12.5px] text-fg transition-colors hover:border-accent-line"
              >
                <Download aria-hidden="true" className="size-3.5" />
                {OS_WORD[entry.os] ?? entry.os} {entry.arch}
              </a>
            ))}
          </div>
          {unix ? (
            <CodeBlock
              label="Or from a terminal"
              value={`curl -fLo kubemg ${server}${unix.path} && chmod +x kubemg && sudo mv kubemg /usr/local/bin/`}
              wrap
            />
          ) : null}
        </div>
      ) : showInstall ? (
        <p className="text-[13px] leading-relaxed text-muted">
          Download <span className="font-mono">kubemg</span> for your system from the{' '}
          <a
            className="text-accent underline-offset-2 hover:underline"
            href={downloads?.releases_url ?? CLI_RELEASES_URL}
            target="_blank"
            rel="noreferrer"
          >
            releases page
          </a>{' '}
          and put it on your PATH — one static binary, no dependencies.
        </p>
      ) : null}
      <CodeBlock label="Sign in (once a day)" value={`kubemg login --server ${server}`} wrap />
      <CodeBlock
        label="Add the cluster to your kubeconfig"
        value={`kubemg update-kubeconfig --cluster ${cluster ?? '<cluster>'}`}
        wrap
      />
      <p className="text-[12.5px] leading-relaxed text-muted">
        Then use kubectl, k9s or helm as usual. kubemg hands them a fifteen-minute credential at a
        time and nothing long-lived is written to disk.
      </p>
    </div>
  )
}
