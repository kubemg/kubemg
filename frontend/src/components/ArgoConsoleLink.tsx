import { ExternalLink } from 'lucide-react'

/*
 * A link into Argo CD's own console — never an embed and never a proxy. The
 * address is built by the server from the cluster's Argo CD console row, after
 * the record was narrowed to the caller, so a record nobody here may see never
 * carries one. No address, no link: absent, not disabled.
 */

export function ArgoConsoleLink({
  href,
  label = 'Open in Argo CD',
  ariaLabel,
  className = '',
}: {
  href?: string
  label?: string
  /** The link's accessible name, when the visible label is shorter than one. */
  ariaLabel?: string
  className?: string
}) {
  if (!href) return null
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      title={href}
      aria-label={ariaLabel}
      onClick={(event) => event.stopPropagation()}
      className={`inline-flex max-w-full items-center gap-1.5 text-[12.5px] font-medium text-accent transition-colors hover:text-accent-hover ${className}`}
    >
      <span className="truncate">{label}</span>
      <ExternalLink aria-hidden="true" className="size-3.5 shrink-0" />
    </a>
  )
}
