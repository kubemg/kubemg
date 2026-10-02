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

Nothing loops. `LinkStatus` uses four static icons for live, direct, down and
idle. There is no travelling pulse and no marquee; the only repeating motion is a
single breathing indicator on a genuinely open stream. The chrome's state
changes — the navigation pill filling, a rail chip on hover — ease over 300 to
500ms on colour and opacity only, and switch off under `prefers-reduced-motion`.

Three typefaces, all self-hosted from `public/fonts`: Inter for the interface and
for data (`font-data` — identifiers and figures; a figure compared down a column
adds `tabular-nums`, which is never set on a name because Inter's widens the
hyphen), Archivo
only for the `kubemg` wordmark, and Commit Mono (`font-mono`) only for code:
YAML, logs, commands, patterns and diff values, where characters have to line up.
There are no font CDN calls, and adding one would be a privacy regression rather
than a convenience.

The sidebar is one card floating 12px in from the window: the cluster rail and
the panel side by side. The row you are on, and the row under the pointer, fill
with the `nav-pill` utility (a lime gradient with an ink label, lit by
`aria-current`); the rail's current cluster carries the `rail-arc` corner.

A dashboard opens on a `slab` — the one dark plate on a page, ink into moss on
both decks, with its own `slab-*` text and state tokens and `Button`'s `slab`
variant for actions on it — and states its facts as `StatTile`s, which take a
tone's soft fill only when the reading is asking for something.
A `StatTile` can also be a link onto what it counts, or a toggle that filters
the list under it, and can carry a bar or a breakdown below its reading.

A list page (the Administration pages are the pattern) says what it is for in
`AppShell`'s `description`, under the title, rather than in an info `Notice` at
the top of its body; opens on a row of `StatTile`s counted from the rows it
holds; and draws a person as an `Avatar`. A `Notice` is for something the
reader should act on or know right now, and leads with its tone's glyph.

Every overlay is one family. A `Sheet` is the editing surface; a `Dialog` is a
centred question with two answers, which is what a confirmation is. Both join
the same overlay stack: only the topmost answers Escape and holds Tab, focus
moves in when it opens and back to the opener when it closes. A destructive
`Dialog` is an `alertdialog` and opens on Cancel. Navigation that should look
like a button is a `LinkButton`, never a `Button` inside a `Link`.

Charts are drawn by hand in SVG (`MetricsChart`'s `Plot`): each series is a
line over an area fading from its own slot colour, the latest sample is
marked, gridlines are dashed, and the legend carries each series' latest
reading.

A list page keeps its filters in the query string through `lib/urlState`
(`useUrlText`, `useUrlFlag`, `useUrlList`), so a narrowed list survives a
reload and is a link. Writes replace the history entry and read the address as
it is now, so two in one handler compose. A form with unsaved edits calls
`useUnsavedGuard(dirty)`: leaving the console is asked about by the browser,
and following a link inside it is asked about on the console's own dialog.
A form's submit button stays enabled until the request starts; a field that
cannot be saved is pointed at — its error shown, focus moved to it — when the
form is submitted. A row in a list that can run to hundreds takes the
`defer-row` utility, so the browser skips laying out the rows off screen.

## Testing

```bash
make frontend-test
```

A derivation gets a `.test.ts` beside its module. A component assertion gets a
`.test.tsx` with `@vitest-environment jsdom` in its own docblock — vitest runs
`node` per file by default, so a component test without the docblock fails on
`document`. Anything you would otherwise check by clicking through the console
belongs here instead; see [Building and testing](verify.md#choosing-a-verification-level).
