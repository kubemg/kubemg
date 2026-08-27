import { useState } from 'react'
import { Eye, Loader2 } from 'lucide-react'
import { errorMessage, revealSecretValue } from '../api/client'
import type { Cluster, ConfigEntry, SecretValue } from '../api/types'
import { Button, CodeBlock, Notice, Sheet } from './primitives'

/*
 * Reading one Secret value.
 *
 * Every list in this console returns a Secret's keys and never its values, and
 * that is still true: this sheet holds no value until somebody asks for one, and
 * it asks for exactly one key at a time. It exists because the alternative was
 * not "the value stays in the cluster" — it was an operator running
 * `kubectl get secret -o jsonpath` in a terminal, where the reveal happens with
 * no record at all.
 *
 * So the point of the surface is the accounting, and it is stated here rather
 * than assumed: each reveal is one request, answered only if the caller holds
 * the capability *and* the cluster's own RBAC allows the read, and written into
 * the audit trail — naming the caller, the Secret and the key — before the value
 * is sent. Nothing keeps it afterwards: the response is `no-store`, the route is
 * outside the cached group, and closing this sheet is the end of the copy on
 * this machine.
 */

/** What a reveal costs, said before the first click rather than after it. */
const REVEAL_BLURB =
  'Each value is read on its own and recorded on its own: the audit trail gets a line naming ' +
  'you, this Secret and the key, written before the value is sent. Nothing caches it — closing ' +
  'this sheet is the end of the copy here.'

export function SecretRevealSheet({
  cluster,
  entry,
  onClose,
}: {
  cluster: Cluster
  entry: ConfigEntry
  onClose: () => void
}) {
  const [values, setValues] = useState<Record<string, SecretValue>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState<string | null>(null)

  async function reveal(key: string) {
    if (busy) return
    setBusy(key)
    setErrors((current) => ({ ...current, [key]: '' }))
    try {
      const answer = await revealSecretValue(cluster.id, entry.namespace, entry.name, key)
      setValues((current) => ({ ...current, [key]: answer }))
    } catch (err) {
      // The server's own words. A ServiceAccount token and KubeMG's own agent
      // secret are refused by name, and a caller without the capability is told
      // who grants it — flattening any of those into "could not read" would
      // lose the only part that says what to do next.
      setErrors((current) => ({
        ...current,
        [key]: errorMessage(err, `${key} could not be read.`),
      }))
    } finally {
      setBusy(null)
    }
  }

  const keys = entry.keys ?? []

  return (
    <Sheet
      onClose={onClose}
      eyebrow={`${entry.namespace} · ${entry.type || 'Opaque'}`}
      title={entry.name}
      width="lg"
      footer={
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <Notice tone="warn">{REVEAL_BLURB}</Notice>

        {keys.length === 0 ? (
          <p className="text-[13px] text-muted">This secret holds no keys.</p>
        ) : null}

        {keys.map((key) => {
          const value = values[key]
          const failure = errors[key]
          return (
            <div key={key} className="flex flex-col gap-1.5">
              <div className="flex items-center justify-between gap-3">
                <span className="truncate font-mono text-[12.5px] text-fg">{key}</span>
                {value ? (
                  <span className="shrink-0 text-[12px] text-faint">{value.bytes} bytes</span>
                ) : (
                  <Button
                    variant="ghost"
                    onClick={() => reveal(key)}
                    disabled={busy !== null}
                    aria-label={`Reveal ${key}`}
                  >
                    {busy === key ? (
                      <Loader2 aria-hidden="true" className="size-4 animate-spin" />
                    ) : (
                      <Eye aria-hidden="true" className="size-4" />
                    )}
                    Reveal
                  </Button>
                )}
              </div>

              {failure ? <Notice tone="error">{failure}</Notice> : null}

              {/* A value that is not text is handed back base64 rather than
                  mangled into replacement characters: "is this the right
                  certificate" is not answerable from a broken decode. */}
              {value?.binary ? (
                <>
                  <Notice tone="info">
                    This value is not text. It is shown base64-encoded, exactly as the cluster
                    stores it.
                  </Notice>
                  <CodeBlock value={value.encoded ?? ''} wrap />
                </>
              ) : null}

              {value && !value.binary ? (
                <CodeBlock value={value.value ?? ''} wrap />
              ) : null}
            </div>
          )
        })}
      </div>
    </Sheet>
  )
}
