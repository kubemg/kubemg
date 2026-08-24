#!/usr/bin/env node
/*
 * Measures the deck's colour pairings against WCAG.
 *
 * This exists because the light deck shipped a phase with its quiet text at
 * 2.78:1 and nobody noticed: the dark deck is the default, the numbers lived in
 * a comment, and a comment is not a measurement. So the tokens are read out of
 * `src/index.css` rather than copied here — a second copy of a palette drifts
 * from the first — and the pairings below are the ones components actually
 * build, taken from `primitives.tsx`.
 *
 * Run with `npm run contrast`. It exits non-zero on a violation, so a token
 * edited to look better cannot quietly drop below the floor.
 *
 * What it checks, and the floor each is held to:
 *   text        4.5:1  WCAG AA for normal text. Every quiet tone is text
 *                      somewhere — `label` micro-caps, table heads, a Pill's
 *                      word — and none of them is large enough for the 3:1
 *                      large-text allowance.
 *   chart       3.0:1  reported, not enforced: three light slots sit below it
 *                      deliberately, which is why every chart ships a written
 *                      legend and identity never rests on colour alone.
 *
 * What it deliberately does **not** check is the hairline: `--deck-border` sits
 * at 1.29:1 on the surface in both decks, and holding a divider to 1.4.11's 3:1
 * would put a heavy rule around every panel in the console. That criterion is
 * about the boundary that identifies a *control* and its state, and it exempts
 * decoration — the deck's state is carried by the accent, the semantic tones and
 * the strand's own shape, all of which are measured above.
 *
 * The ΔE figures are CIE76 over Viénot-simulated linear values. They are for
 * comparing this run against the next one, not against a figure from another
 * formula — the absolute numbers differ several-fold between ΔE definitions, so
 * what matters is that the set's worst adjacent pair does not shrink when a slot
 * is touched.
 */

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const CSS = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'index.css')

const TEXT_FLOOR = 4.5
/** 1.4.11: a glyph that carries meaning is a graphical object, not text. */
const GLYPH_FLOOR = 3
const CHART_FLOOR = 3
/** The eight chart slots are a validated set. A ninth is not a colour, it is a
    reordering of the whole set's colour-blindness safety. */
const CHART_SLOTS = 8

/* ------------------------------------------------------------- measurement --- */

function channel(value) {
  const c = value / 255
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
}

function parse(hex) {
  const h = hex.replace('#', '')
  const full =
    h.length === 3
      ? h
          .split('')
          .map((c) => c + c)
          .join('')
      : h
  return [0, 2, 4].map((i) => Number.parseInt(full.slice(i, i + 2), 16))
}

function luminance(hex) {
  const [r, g, b] = parse(hex)
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function contrast(fg, bg) {
  const a = luminance(fg)
  const b = luminance(bg)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

/* ------------------------------------------------------------ colour vision --- */

/*
 * Viénot, Brettel & Mollon (1999): the two dichromacies as a projection in
 * linear RGB. It is an approximation and the literature has better ones, but it
 * is the one that answers the question asked here — are two adjacent slots still
 * two colours to a red-green dichromat — without pulling a colour library into a
 * design system that has none.
 */
const CVD = {
  protanopia: [
    [0.1129, 0.8871, 0],
    [0.1129, 0.8871, 0],
    [0.004, -0.004, 1],
  ],
  deuteranopia: [
    [0.2921, 0.7079, 0],
    [0.2921, 0.7079, 0],
    [-0.0234, 0.0234, 1],
  ],
}

function simulate(hex, matrix) {
  const linear = parse(hex).map(channel)
  return matrix.map((row) => row.reduce((sum, weight, i) => sum + weight * linear[i], 0))
}

/** CIE76 ΔE in Lab, over the linear values the simulation produced. */
function deltaE(a, b) {
  const [l1, a1, b1] = lab(a)
  const [l2, a2, b2] = lab(b)
  return Math.sqrt((l1 - l2) ** 2 + (a1 - a2) ** 2 + (b1 - b2) ** 2)
}

function lab(linear) {
  const [r, g, b] = linear.map((c) => Math.min(1, Math.max(0, c)))
  // sRGB → XYZ (D65), then XYZ → Lab.
  const x = (0.4124 * r + 0.3576 * g + 0.1805 * b) / 0.95047
  const y = 0.2126 * r + 0.7152 * g + 0.0722 * b
  const z = (0.0193 * r + 0.1192 * g + 0.9505 * b) / 1.08883
  const f = (t) => (t > 0.008856 ? Math.cbrt(t) : 7.787 * t + 16 / 116)
  return [116 * f(y) - 16, 500 * (f(x) - f(y)), 200 * (f(y) - f(z))]
}

/* ------------------------------------------------------------------ tokens --- */

/** Reads one `:root` block's `--deck-*` values. The selector is what separates
    the two decks, so it is matched rather than the values being duplicated. */
function tokensFor(css, selector) {
  const start = css.indexOf(selector)
  if (start === -1) throw new Error(`no ${selector} block in index.css`)
  const open = css.indexOf('{', start)
  const close = css.indexOf('\n}', open)
  const block = css.slice(open, close)

  const tokens = {}
  for (const match of block.matchAll(/--deck-([a-z0-9-]+):\s*(#[0-9a-fA-F]{3,8})\s*;/g)) {
    tokens[match[1]] = match[2]
  }
  return tokens
}

/*
 * The pairings. Each one is a combination a component actually renders — the
 * surfaces a tone can land on, the soft background its Pill and Notice use, and
 * the chrome, whose tokens are dark in both decks because the rail is.
 */
const SURFACES = ['surface', 'bg', 'raised', 'sunken']
/* `syntax-scalar` is here rather than with the charts because it is read as
   text — a port number in a manifest — not glanced at as a series identity. */
const TEXT_TONES = ['text', 'muted', 'faint', 'ok', 'warn', 'danger', 'accent', 'syntax-scalar']
const SOFT_TONES = ['ok', 'warn', 'danger', 'accent']

function check(deck, tokens) {
  const failures = []
  const report = []

  const measure = (fg, bg, floor, what) => {
    const value = contrast(tokens[fg], tokens[bg])
    report.push({ what, value, floor })
    if (value < floor) failures.push({ deck, what, value, floor })
    return value
  }

  for (const tone of TEXT_TONES) {
    for (const surface of SURFACES) measure(tone, surface, TEXT_FLOOR, `${tone} on ${surface}`)
  }
  for (const tone of SOFT_TONES) {
    measure(tone, `${tone}-soft`, TEXT_FLOOR, `${tone} on ${tone}-soft`)
  }
  // The rail is chrome with tokens of its own — graphite on the dark deck, a
  // cool grey on the light one — so its tones are held to the floor against the
  // rail rather than against the work surface.
  for (const tone of ['rail-text', 'rail-muted', 'rail-faint']) {
    for (const surface of ['rail', 'rail-raised']) {
      measure(tone, surface, TEXT_FLOOR, `${tone} on ${surface}`)
    }
  }
  // The accent is text on the rail — the `MG` half of the wordmark — so it is
  // held to the text floor there. It lands on `rail-raised` only as the mark's
  // hover glyph, which is where the two below sit as well: a glyph is a
  // graphical object, so 1.4.11's 3:1 is the floor rather than 4.5:1. They are
  // measured because the light deck's rail is now near the top of the ramp,
  // where a dark-deck assumption about "plenty of room" stops holding.
  measure('accent', 'rail', TEXT_FLOOR, 'accent on rail')
  measure('accent', 'rail-raised', GLYPH_FLOOR, 'accent glyph on rail-raised')
  // A cluster's link state is a glyph on every rail row — `ok` where a tunnel
  // is up, `danger` where it is down. The two neutral link states take the
  // rail's own tokens, measured just above; these two are state, they stay
  // semantic wherever they land, so they are measured where they land.
  for (const tone of ['ok', 'danger']) {
    for (const surface of ['rail', 'rail-raised']) {
      measure(tone, surface, GLYPH_FLOOR, `${tone} glyph on ${surface}`)
    }
  }
  // A primary button is lime with ink on it, on both decks — `accent-fill`
  // rather than `accent`, which on the light deck is the same hue taken down the
  // ramp so it can be read as text.
  measure('on-accent', 'accent-fill', TEXT_FLOOR, 'on-accent on accent-fill')

  // The mark is lime on a chip that is ink where the deck needs one and nothing
  // where it does not. Measured where there is a chip to measure against — on
  // the dark deck the chip is `transparent`, which is not a hex and so is not a
  // token this reads, and the mark sits on the rail and the surface instead.
  if (tokens['mark-chip']) {
    measure('accent-fill', 'mark-chip', GLYPH_FLOOR, 'mark on its chip')
  } else {
    for (const surface of ['rail', 'surface']) {
      measure('accent-fill', surface, GLYPH_FLOOR, `mark on ${surface}`)
    }
  }

  // 1.4.11 wants a control's boundary discernible. On ink, lime does that by
  // itself; on bone it is 1.25:1 and cannot, which is why the button draws an
  // `accent` hairline there. So the boundary is measured only on the deck that
  // needs one — where the fill does not clear the glyph floor against the
  // surface, the hairline has to, and a deck that stops needing it stops being
  // asked. Skipping it outright would let a light-deck lime button lose its
  // outline unnoticed.
  if (contrast(tokens['accent-fill'], tokens.surface) < GLYPH_FLOOR) {
    measure('accent', 'accent-fill', GLYPH_FLOOR, 'accent hairline on accent-fill')
  }

  return { failures, report }
}

function charts(deck, tokens) {
  const slots = []
  for (let i = 1; ; i += 1) {
    const token = tokens[`chart-${i}`]
    if (!token) break
    slots.push(token)
  }

  const notes = []
  const failures = []
  if (slots.length !== CHART_SLOTS) {
    failures.push({
      deck,
      what: `${slots.length} chart slots`,
      value: slots.length,
      floor: CHART_SLOTS,
    })
  }

  // Against the surface a chart is drawn on. Below the floor is a *known*
  // state here, so it is reported rather than failed — see the header.
  const below = slots.filter((slot) => contrast(slot, tokens.surface) < CHART_FLOOR)
  notes.push(`${below.length} of ${slots.length} slots below ${CHART_FLOOR}:1 on the surface`)

  // Adjacent pairs are what the slot order protects: a chart hands out slots in
  // order, so slots 3 and 4 land side by side far more often than 3 and 7.
  const adjacency = (transform) => {
    let worst = Infinity
    for (let i = 1; i < slots.length; i += 1) {
      worst = Math.min(worst, deltaE(transform(slots[i - 1]), transform(slots[i])))
    }
    return worst
  }
  notes.push(`worst adjacent ΔE — normal ${adjacency((s) => parse(s).map(channel)).toFixed(1)}`)
  for (const [name, matrix] of Object.entries(CVD)) {
    notes.push(`${name} ${adjacency((s) => simulate(s, matrix)).toFixed(1)}`)
  }

  return { failures, notes }
}

/* -------------------------------------------------------------------- main --- */

const css = await readFile(CSS, 'utf8')
const decks = {
  light: tokensFor(css, ':root {'),
  dark: tokensFor(css, ":root[data-theme='dark'] {"),
}

let failures = []
for (const [deck, tokens] of Object.entries(decks)) {
  const text = check(deck, tokens)
  const chart = charts(deck, tokens)
  failures = failures.concat(text.failures, chart.failures)

  const tightest = [...text.report].sort((a, b) => a.value - b.value).slice(0, 3)
  console.log(`\n${deck} deck — ${text.report.length} pairings, tightest:`)
  for (const entry of tightest) {
    console.log(`  ${entry.value.toFixed(2)}:1  ${entry.what} (floor ${entry.floor})`)
  }
  console.log(`  charts: ${chart.notes.join(', ')}`)
}

if (failures.length > 0) {
  console.error(`\n${failures.length} pairing(s) below the floor:`)
  for (const entry of failures) {
    console.error(`  ${entry.deck}: ${entry.what} — ${Number(entry.value).toFixed(2)} < ${entry.floor}`)
  }
  process.exit(1)
}
console.log('\nEvery pairing clears its floor.')
