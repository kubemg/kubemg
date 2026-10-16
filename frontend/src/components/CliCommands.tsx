import { CodeBlock } from './primitives'

/** Where the CLI is downloaded from. */
const CLI_RELEASES_URL = 'https://github.com/kubemg/kubemg/releases'

/**
 * The commands that give kubectl access through the kubemg CLI, with this
 * server's address and the cluster's name filled in — rendered from what the
 * server says rather than typed by whoever reads them, the agent install
 * sheet's reason: commands a person retypes are commands that drift.
 */
export function CliCommands({
  serverURL,
  cluster,
  showInstall = false,
}: {
  serverURL: string
  cluster?: string
  /** Say where the binary comes from, for somebody who has never used it. */
  showInstall?: boolean
}) {
  const server = serverURL || window.location.origin
  return (
    <div className="flex flex-col gap-3">
      {showInstall ? (
        <p className="text-[13px] leading-relaxed text-muted">
          Download <span className="font-mono">kubemg</span> for your system from the{' '}
          <a className="text-accent underline-offset-2 hover:underline" href={CLI_RELEASES_URL} target="_blank" rel="noreferrer">
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
