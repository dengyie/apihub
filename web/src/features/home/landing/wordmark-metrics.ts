/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/** Reference band: the width the mark aims to fill, and its metrics at 260px. */
const BAND_WIDTH = 1120
const BASE_FONT_SIZE = 260
const BASE_HEIGHT = 210
const BASELINE_RATIO = 205 / 210
const MIN_FONT_SIZE = 96
const MAX_FONT_SIZE = 300
const SLACK = 1.02
/** Stripe geometry, shared with the renderer so rows and rects always agree. */
export const STRIPE_STEP = 6
export const STRIPE_THICKNESS = 2

/**
 * Approximate advance width of a string, in em.
 *
 * SVG cannot measure text before layout, and the operator's `system_name` is
 * arbitrary — Latin, CJK, or a mix. The previous version forced every name to
 * exactly `BAND_WIDTH` with `lengthAdjust="spacingAndGlyphs"`, which squashed
 * or stretched glyphs by up to 2x depending on length. Estimating lets the
 * font size absorb the width instead, so glyphs keep natural proportions at
 * any name length.
 */
export function estimateEmWidth(text: string): number {
  let em = 0
  for (const char of text) {
    if (/\s/.test(char)) em += 0.3
    else if (/[⺀-鿿豈-﫿＀-￯]/.test(char)) em += 1
    // Narrow glyphs are checked before the uppercase case, or `I` and `J`
    // would be charged full uppercase width.
    else if (/[iljtfIJ.,:;'"!|()[\]]/.test(char)) em += 0.3
    else if (/[A-Z]/.test(char)) em += 0.68
    else em += 0.55
  }
  // A single-character or empty name would otherwise collapse the band.
  return Math.max(em, 1.5)
}

type WordmarkMetrics = {
  fontSize: number
  width: number
  height: number
  baseline: number
  rows: number
}

export function computeWordmarkMetrics(brand: string): WordmarkMetrics {
  const em = estimateEmWidth(brand)
  // Letting the font size absorb the name length is what keeps glyphs at their
  // natural proportions: an unclamped size makes the text span the band by
  // construction instead of being squeezed into it.
  const ideal = BAND_WIDTH / em
  // Round before deriving the width, so the band always matches the size the
  // browser actually renders.
  const fontSize = Math.round(
    Math.min(Math.max(ideal, MIN_FONT_SIZE), MAX_FONT_SIZE)
  )
  const natural = em * fontSize
  // The band is exactly BAND_WIDTH whenever the text fits at this size. Past
  // that it grows — with 2% slack so the clip does not shave the last glyph —
  // rather than clipping the ends.
  const width =
    natural > BAND_WIDTH * 1.001 ? Math.ceil(natural * SLACK) : BAND_WIDTH
  const height = Math.round(BASE_HEIGHT * (fontSize / BASE_FONT_SIZE))

  return {
    fontSize,
    width,
    height,
    baseline: Math.round(height * BASELINE_RATIO),
    rows: Math.ceil(height / STRIPE_STEP),
  }
}
