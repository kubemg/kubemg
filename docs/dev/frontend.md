# Console internals

Vite, React, TypeScript, Tailwind v4, react-router and xterm. In development it
runs as a Vite dev server proxying `/api`; in a production build it is compiled
and embedded into the Go binary, which is why a production install needs no CORS
configuration.

```
frontend/src/
  pages/        one file per route
  components/   sheets, panels, tables, the rail
  lib/          derivations — pure TypeScript, no React
  api/          the axios client and typed calls
  state/        session and cluster context
  index.css     the design tokens
```

## Where logic belongs

`lib/` is pure: no React, no DOM. That is what makes it testable in the `node`
environment and what keeps a derivation from being re-implemented in three
components. The pattern to follow:

| Module | Owns |
|---|---|
| `lib/resources.ts` | The Explore inventory — the fixed kinds, and the CRD sections discovered per cluster |
| `lib/insights.ts` | Pod bucketing by phase and readiness, and the alert list |
| `lib/objectForm.ts` | The seven-kind create form, and the YAML it writes |
| `lib/live.ts` | Polling cadences and the rules for a tick |
| `lib/favorites.ts` | Starred kinds, in browser storage |

Two details in `resources.ts` that look like edge cases and are not: a CRD family
needs at least two kinds to earn its own sidebar section, a single-kind family
falls to **Other** at the bottom, and discovered sections sit **below** the fixed
inventory and start collapsed. And `crds === null` (discovery still running) is
not `[]` (there are none) — the first waits, the second falls back.

## Reading data

`useCachedQuery` is the read hook. It is deliberately **not** React Query or SWR:
the cache it needs is the same short-TTL, identity-keyed cache the server
implements, and matching it exactly was cheaper than configuring a library into
the same shape.

Live reads (`lib/live.ts`) follow four rules:

- Ticks run **only while somebody is looking**.
- A tick is invisible: it never draws a skeleton.
- A failed tick **keeps what is on screen** and reports staleness.
- A tick does not send `Cache-Control: no-cache`. **Refresh** stays the thing
  that actually asks the cluster.

State reads as a word, never a spinner. The YAML tab, the logs and terminal tab,
the fleet capacity fan-out and the wizard's handshake step are deliberately not
live.

Streaming reads — logs and the terminal — use `fetch` and `WebSocket` against the
proxy URL directly, bypassing axios. `PodTerminal` is **lazy-loaded**, because
xterm is about 290 kB and must not sit in the main bundle.

## Surfaces

Every editing surface is a `Sheet`. Widths are `md`, `lg`, `xl`, `2xl` and
`wide` — 520, 680, 900, 1100 pixels and 85vw. A new modal shape is almost always
a `Sheet` that has not been recognised as one.

Two exceptions exist and both are deliberate. **Cluster registration is a page,
not a drawer** (`/clusters/new`), because it is a five-step process where the
record is created halfway through and steps one and two lock afterwards.
**`ShellDock` mounts above the router**, because inside the app shell it would be
torn down on every navigation — it is not a page and has no address.

There is no third navigation level. A 60-pixel icon rail carries three sections
and opens a 240-pixel panel; anything deeper goes in the page's own panel.

## The design system

The visual language is defined by tokens in `src/index.css`. **Never hard-code a
hex and never add a one-off colour.**

| Token | Value | Role |
|---|---|---|
| ink | `#14161A` | The dark ground |
| bone | `#F2F3EF` | The light ground |
| lime | `#BFF23C` | The **only** interactive accent |
| moss | `#3A4033` | Structure |
| sage / amber / rust | `#7FB069` / `#E8A33D` / `#D1553C` | Semantic state only — never interaction |

Text on lime is **always ink, never white**. The wordmark is always lowercase.
Radii are 12, 8 and 6 pixels for card, control and chip.

Every text tone must clear 4.5:1 on every surface in both light and dark, and
`make frontend-contrast` fails the build if it does not. **Fix a violation by
moving the token, never by adding an exception in a component.**

Charts use the categorical palette `--chart-1` through `--chart-8`. Slot order is
the colour-blindness mechanism: never reorder it and never add a ninth.

Animation is close to banned. `LinkStatus` uses four static icons for live,
direct, down and idle. There is no travelling pulse, no marquee, and nothing else
animates except a single breathing indicator on a genuinely open stream.

Two typefaces, Archivo for text and Commit Mono for code, both self-hosted from
`public/fonts`. There are no font CDN calls, and adding one would be a privacy
regression rather than a convenience.

## Testing

```bash
make frontend-test
```

A derivation gets a `.test.ts` beside its module. A component assertion gets a
`.test.tsx` with `@vitest-environment jsdom` in its own docblock — vitest runs
`node` per file by default, so a component test without the docblock fails on
`document`. Anything you would otherwise check by clicking through the console
belongs here instead; see [Building and testing](verify.md#choosing-a-verification-level).
