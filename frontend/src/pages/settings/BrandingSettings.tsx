import { useCallback, useEffect, useRef, useState } from 'react'
import type { ChangeEvent, FormEvent } from 'react'
import { RotateCcw, Trash2, Upload } from 'lucide-react'
import { errorMessage, updateBranding } from '../../api/client'
import type { BannerTone, Branding } from '../../api/types'
import { BANNER_TONE_CLASS, bannerTone, hasBanner } from '../../lib/branding'
import { Button, Field, Notice, Panel, Select, TextInput } from '../../components/primitives'
import { Lockup } from '../../components/Mark'
import { SettingsAside, SettingsLayout } from '../../components/settings/SettingsLayout'
import { useBranding } from '../../state/branding-context'

/**
 * Where a customer puts their own name on their console.
 *
 * The three things this writes are not decoration, and they are not the same
 * kind of thing as each other:
 *
 *   * The **organisation name and mark** are how an internal platform team
 *     introduces a tool. A console that carries only its vendor's identity is one
 *     that reads as somebody else's software running in the middle of your
 *     access path.
 *   * The **banner** is operational. It is how an operator tells a production
 *     console from a staging one *before* typing into it, which is why it
 *     renders on the sign-in page and not only after a session exists.
 *   * The **footer notice** is the handling caveat a regulated site is obliged
 *     to state on every screen.
 *
 * The brand rules hold through all of it and the form says so where it matters:
 * the lockup stays lowercase `kubemg` and an organisation's mark sits beside it
 * rather than replacing it, and the banner's tones are the deck's semantic three
 * — never lime, which means "you can press this".
 */
export function BrandingSettings() {
  const { branding, refresh } = useBranding()

  const [draft, setDraft] = useState<Branding>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const markInput = useRef<HTMLInputElement>(null)

  // The provider is the source of truth; this form edits a copy of it, so a save
  // made in another tab is not silently overwritten by a stale draft on submit.
  useEffect(() => {
    if (branding) setDraft(normalize(branding))
  }, [branding])

  const set = useCallback(<K extends keyof Branding>(key: K, value: Branding[K]) => {
    setDraft((current) => ({ ...current, [key]: value }))
    setSaved(false)
  }, [])

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      // Every field is sent on every save, including the empty ones: this form
      // is the whole of the branding, so an emptied field means "clear it"
      // rather than "leave it alone".
      await updateBranding({
        organisation_name: draft.organisation_name ?? '',
        organisation_mark: draft.organisation_mark ?? '',
        banner_text: draft.banner_text ?? '',
        banner_tone: draft.banner_tone ?? 'neutral',
        footer_notice: draft.footer_notice ?? '',
      })
      await refresh()
      setSaved(true)
    } catch (err) {
      setError(errorMessage(err, 'Could not save the branding.'))
    } finally {
      setBusy(false)
    }
  }

  /**
   * A picked file becomes a data: URI here, in the browser, and is sent as one.
   *
   * There is deliberately no upload endpoint and no volume: the mark travels
   * with the database that already has to be backed up, and a console with no
   * route to the internet renders it exactly as one with a route does. The
   * server refuses an SVG and anything over 64 KB; this only saves a round trip
   * for the second of those.
   */
  function pickMark(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return
    if (file.size > MAX_MARK_BYTES) {
      setError(`That image is ${Math.round(file.size / 1024)} KB. The mark must be 64 KB or smaller.`)
      return
    }
    const reader = new FileReader()
    reader.onerror = () => setError('Could not read that file.')
    reader.onload = () => {
      const result = reader.result
      if (typeof result === 'string') {
        setError(null)
        set('organisation_mark', result)
      }
    }
    reader.readAsDataURL(file)
  }

  const stored = branding ? normalize(branding) : null
  const dirty = stored !== null && !same(stored, normalize(draft))

  return (
    <SettingsLayout
      title="Branding"
      aside={
        <>
          <SettingsAside
            label="Sign-in page"
            value={hasBanner(draft) ? 'Carries the banner' : 'No banner'}
            reach="The banner and the organisation's mark are both drawn before anybody signs in — which is the point of a banner, and why this is the one part of the console a stranger can see."
          />
          <SettingsAside
            label="Every page"
            value={draft.footer_notice?.trim() ? draft.footer_notice : 'No notice'}
            reach="The footer line sits under the work on every page, beside the release this console is running."
          />
        </>
      }
      actions={
        <>
          <Button
            type="button"
            variant="ghost"
            disabled={busy || !dirty}
            onClick={() => {
              if (stored) setDraft(stored)
              setSaved(false)
              setError(null)
            }}
          >
            <RotateCcw aria-hidden="true" className="size-4" />
            Discard
          </Button>
          <Button type="submit" form="branding-form" variant="primary" disabled={busy || !dirty}>
            {busy ? 'Saving…' : 'Save branding'}
          </Button>
        </>
      }
    >
      <form id="branding-form" onSubmit={save} className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {saved && !dirty ? <Notice tone="ok">Saved.</Notice> : null}

        <Panel
          eyebrow="Identity"
          title="Whose installation this is"
          description="Your organisation's name and mark, beside the kubemg lockup rather than in place of it — a console that presents itself as somebody else's product is one nobody can get support for."
          bodyClassName="flex flex-col gap-4 p-4"
        >
          <Field
            label="Organisation name"
            htmlFor="organisation_name"
            hint="Shown beside the lockup on the sign-in page and in the footer. Leave it empty for none."
          >
            <TextInput
              id="organisation_name"
              maxLength={60}
              placeholder="Acme Platform Engineering"
              value={draft.organisation_name ?? ''}
              onChange={(event) => set('organisation_name', event.target.value)}
            />
          </Field>

          <div className="flex flex-col gap-1.5">
            <p className="label">Mark</p>
            <div className="flex flex-wrap items-center gap-3">
              {draft.organisation_mark ? (
                <img
                  src={draft.organisation_mark}
                  alt=""
                  className="size-10 shrink-0 rounded-control border border-line object-contain"
                />
              ) : (
                <span className="grid size-10 shrink-0 place-items-center rounded-control border border-dashed border-line text-[11px] text-faint">
                  none
                </span>
              )}
              <Button type="button" variant="secondary" onClick={() => markInput.current?.click()}>
                <Upload aria-hidden="true" className="size-4" />
                Choose an image
              </Button>
              {draft.organisation_mark ? (
                <Button type="button" variant="ghost" onClick={() => set('organisation_mark', '')}>
                  <Trash2 aria-hidden="true" className="size-4" />
                  Remove
                </Button>
              ) : null}
              <input
                ref={markInput}
                type="file"
                accept="image/png,image/jpeg,image/gif,image/webp"
                className="sr-only"
                onChange={pickMark}
              />
            </div>
            <p className="text-[12px] text-muted">
              A PNG, JPEG, GIF or WebP up to 64 KB, stored in the database rather than fetched from
              anywhere — so it renders in an air-gapped install, and the sign-in page never tells a
              third party who is looking at it. SVG is refused: it can carry script, and this is
              drawn for people who have not signed in yet.
            </p>
          </div>

          <Preview name={draft.organisation_name ?? ''} mark={draft.organisation_mark ?? ''} />
        </Panel>

        <Panel
          eyebrow="Environment"
          title="A banner across every page"
          description="One line saying which installation this is. It is drawn before sign-in, because the keystrokes worth protecting are the ones going into the password field."
          bodyClassName="flex flex-col gap-4 p-4"
        >
          <Field
            label="Banner"
            htmlFor="banner_text"
            hint="Leave it empty for no banner — which is the default, and the right answer for most installs. A console that always carries a banner is one where the banner stops being read."
          >
            <TextInput
              id="banner_text"
              maxLength={120}
              placeholder="PRODUCTION — changes here affect customers"
              value={draft.banner_text ?? ''}
              onChange={(event) => set('banner_text', event.target.value)}
            />
          </Field>

          <Field
            label="Tone"
            htmlFor="banner_tone"
            hint="The deck's semantic colours, so amber means here what it means on a pod. Lime is not offered: it is the interactive accent, and a banner is not something you press."
          >
            <Select
              id="banner_tone"
              className="max-w-52"
              value={draft.banner_tone ?? 'neutral'}
              onChange={(event) => set('banner_tone', event.target.value as BannerTone)}
            >
              <option value="neutral">Neutral — a statement of fact</option>
              <option value="caution">Caution — handle with care</option>
              <option value="critical">Critical — a mistake here is expensive</option>
            </Select>
          </Field>

          {hasBanner(draft) ? (
            <div className="flex flex-col gap-1.5">
              <p className="label">As drawn</p>
              <div
                className={`flex items-center justify-center rounded-control border px-4 py-1.5 text-center text-[12.5px] font-medium tracking-[0.02em] ${
                  BANNER_TONE_CLASS[bannerTone(draft)]
                }`}
              >
                <span className="min-w-0 truncate">{draft.banner_text}</span>
              </div>
            </div>
          ) : null}
        </Panel>

        <Panel
          eyebrow="Handling"
          title="A line under every page"
          description="The classification or handling caveat a site is obliged to state. It sits beside the release number in the footer."
          bodyClassName="flex flex-col gap-4 p-4"
        >
          <Field
            label="Footer notice"
            htmlFor="footer_notice"
            hint="Leave it empty for none."
          >
            <TextInput
              id="footer_notice"
              maxLength={160}
              placeholder="Internal — Restricted"
              value={draft.footer_notice ?? ''}
              onChange={(event) => set('footer_notice', event.target.value)}
            />
          </Field>
        </Panel>
      </form>
    </SettingsLayout>
  )
}

/** The client's copy of the server's ceiling, so a file too large is refused
    before it is read into memory and posted. */
const MAX_MARK_BYTES = 64 * 1024

/** How the identity reads where it is actually drawn. It is a real Lockup rather
    than a picture of one, so the rule it demonstrates — the mark beside the
    wordmark, never instead of it — cannot drift from the rule the console
    follows. */
function Preview({ name, mark }: { name: string; mark: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <p className="label">On the sign-in page</p>
      <div className="flex items-center gap-3 rounded-control bg-raised px-4 py-3">
        <Lockup className="text-[17px] text-fg" />
        {name || mark ? (
          <>
            <span aria-hidden="true" className="h-5 w-px shrink-0 bg-line" />
            {mark ? <img src={mark} alt="" className="size-6 shrink-0 object-contain" /> : null}
            {name ? <span className="min-w-0 truncate text-[13px] text-muted">{name}</span> : null}
          </>
        ) : null}
      </div>
    </div>
  )
}

/** The draft as the server would store it, so "is this dirty" compares like with
    like rather than treating undefined and "" as different answers. */
function normalize(branding: Branding): Branding {
  return {
    organisation_name: (branding.organisation_name ?? '').trim(),
    organisation_mark: (branding.organisation_mark ?? '').trim(),
    banner_text: (branding.banner_text ?? '').trim(),
    banner_tone: branding.banner_tone ?? 'neutral',
    footer_notice: (branding.footer_notice ?? '').trim(),
  }
}

function same(a: Branding, b: Branding): boolean {
  return (
    a.organisation_name === b.organisation_name &&
    a.organisation_mark === b.organisation_mark &&
    a.banner_text === b.banner_text &&
    a.banner_tone === b.banner_tone &&
    a.footer_notice === b.footer_notice
  )
}
