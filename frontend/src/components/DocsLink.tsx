import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'

import { fetchServerVersion } from '../api/client'
import { DEFAULT_DOCS_BASE, docsHref } from '../lib/docs'
import type { DocTarget } from '../lib/docs'

/* The server says where its manual lives. One read serves every link on the
   page; a failed read is forgotten so the next mount asks again, and until an
   answer arrives a link points at the default manual rather than at nothing. */
let base: Promise<string> | null = null

function readDocsBase(): Promise<string> {
  base ??= fetchServerVersion()
    .then((answer) => answer.docs_url || DEFAULT_DOCS_BASE)
    .catch(() => {
      base = null
      return DEFAULT_DOCS_BASE
    })
  return base
}

export function useDocsBase(): string {
  const [value, setValue] = useState(DEFAULT_DOCS_BASE)
  useEffect(() => {
    let live = true
    void readDocsBase().then((answer) => {
      if (live) setValue(answer)
    })
    return () => {
      live = false
    }
  }, [])
  return value
}

/**
 * A link into the manual, where the reasoning behind a setting lives. The
 * console says what a field does in a line; this is the door to the rest.
 */
export function DocsLink({ to, children = 'Read more' }: { to: DocTarget; children?: ReactNode }) {
  const docs = useDocsBase()
  return (
    <a
      href={docsHref(docs, to)}
      target="_blank"
      rel="noreferrer noopener"
      className="inline-flex items-center gap-1 text-[12.5px] font-medium text-accent transition-colors hover:text-accent-hover motion-reduce:transition-none"
    >
      {children}
      <ExternalLink aria-hidden="true" className="size-3.5 shrink-0" />
      <span className="sr-only"> (opens the manual in a new tab)</span>
    </a>
  )
}
