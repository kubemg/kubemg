import { useId, useState } from 'react'
import { ExternalLink } from 'lucide-react'

import { Button, Field, Select, Sheet, TextArea, TextInput } from './primitives'

/** Where feedback goes: the project's own discussions on GitHub. */
const DISCUSSIONS_NEW_URL = 'https://github.com/kubemg/kubemg/discussions/new'

/** The discussion categories feedback is offered under, by their slugs. */
const CATEGORIES = [
  { slug: 'ideas', label: 'Idea' },
  { slug: 'q-a', label: 'Question' },
  { slug: 'general', label: 'General' },
] as const

/**
 * Past this the address GitHub is opened on grows longer than a browser or
 * GitHub will carry, so the message is capped well under it.
 */
const MESSAGE_LIMIT = 4000

/**
 * discussionURL is GitHub's new-discussion page with the feedback already in
 * it. The release is appended because it is the first thing anybody answering
 * asks; nothing else about the install is.
 */
function discussionURL(category: string, title: string, message: string, version?: string): string {
  const body = version ? `${message.trim()}\n\n---\nkubemg ${version}` : message.trim()
  const params = new URLSearchParams({ category, title: title.trim(), body })
  return `${DISCUSSIONS_NEW_URL}?${params.toString()}`
}

/**
 * FeedbackSheet takes a note for the kubemg project and hands it to GitHub
 * Discussions. The server sends nothing: GitHub opens in a new tab with the
 * note filled in, and it is posted from the reader's own GitHub account. That
 * is what keeps it working on an install with no route to GitHub and no token
 * to hold, and it leaves who posts, and whether, with the person posting.
 */
export function FeedbackSheet({ version, onClose }: { version?: string; onClose: () => void }) {
  const id = useId()
  const [category, setCategory] = useState<string>(CATEGORIES[0].slug)
  const [title, setTitle] = useState('')
  const [message, setMessage] = useState('')
  const ready = title.trim() !== '' && message.trim() !== ''

  function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!ready) return
    window.open(discussionURL(category, title, message, version), '_blank', 'noopener,noreferrer')
    onClose()
  }

  return (
    <Sheet
      eyebrow="kubemg project"
      title="Send feedback"
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!ready}>
            Continue on GitHub
            <ExternalLink aria-hidden="true" className="ml-1.5 size-3.5" />
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p className="text-[13px] leading-relaxed text-muted">
          Your note opens as a new discussion on the kubemg GitHub repository, ready to post from
          your own GitHub account. Nothing is sent from this server, and the discussion is public:
          leave out cluster names, addresses and anything else about your install.
        </p>
        <Field label="Kind" htmlFor={`${id}-category`}>
          <Select id={`${id}-category`} value={category} onChange={(event) => setCategory(event.target.value)}>
            {CATEGORIES.map((option) => (
              <option key={option.slug} value={option.slug}>
                {option.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Title" htmlFor={`${id}-title`}>
          <TextInput
            id={`${id}-title`}
            value={title}
            maxLength={200}
            onChange={(event) => setTitle(event.target.value)}
            autoFocus
          />
        </Field>
        <Field
          label="Message"
          htmlFor={`${id}-message`}
          hint={`${message.length} / ${MESSAGE_LIMIT}`}
        >
          <TextArea
            id={`${id}-message`}
            prose
            rows={8}
            value={message}
            maxLength={MESSAGE_LIMIT}
            onChange={(event) => setMessage(event.target.value)}
          />
        </Field>
      </div>
    </Sheet>
  )
}
