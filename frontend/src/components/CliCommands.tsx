import { CodeBlock } from './primitives'

/**
 * The two commands that give kubectl access through the kubemg CLI, with this
 * server's address and the cluster's name filled in — rendered from what the
 * server says rather than typed by whoever reads them, the agent install
 * sheet's reason: commands a person retypes are commands that drift.
 */
export function CliCommands({ serverURL, cluster }: { serverURL: string; cluster?: string }) {
  const server = serverURL || window.location.origin
  return (
    <div className="flex flex-col gap-3">
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
