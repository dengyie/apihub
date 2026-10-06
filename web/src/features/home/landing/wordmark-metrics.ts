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
/** Reference band: the mark is clipped against a box of exactly this size at
 *  every brand length, so the footer's height never depends on the operator's
 *  `system_name`. Only the font size inside the box varies. */
const BAND_WIDTH = 1120
const BAND_HEIGHT = 210
/** Cap height as a fraction of the font size, used to centre the glyphs. */
const CAP_HEIGHT_RATIO = 0.7
/** Tracking in em, mirrored by `letter-spacing` in `landing.css`. Negative
 *  values tighten the mark, as at the 260px reference size where this was
 *  -18px. */
const TRACKING_EM = -0.07
/** Largest size the mark ever takes, so a short name cannot outgrow the band. */
const MAX_FONT_SIZE = 260
/** Stripe geometry, shared with the renderer so rows and rects always agree. */
export const STRIPE_STEP = 6
export const STRIPE_THICKNESS = 2

/**
 * Approximate advance width of a string, in em, tracking included.
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
  // Tracking applies between glyphs only, and the floor keeps a single-character
  // or empty name from dividing the band away.
  const tracked = em + TRACKING_EM * Math.max(text.length - 1, 0)
  return Math.max(tracked, 1.5)
}

type WordmarkMetrics = {
  fontSize: number
  width: number
  height: number
  baseline: number
  rows: number
}

export function computeWordmarkMetrics(brand: string): WordmarkMetrics {
  // Floor rather than round the size, so the natural width can never outgrow
  // the box the text is clipped against. A long name therefore renders as a
  // smaller mark inside the same band, not as a wider, flatter one.
  const fontSize = Math.max(
    Math.min(Math.floor(BAND_WIDTH / estimateEmWidth(brand)), MAX_FONT_SIZE),
    1
  )

  return {
    fontSize,
    width: BAND_WIDTH,
    height: BAND_HEIGHT,
    // Centred on the cap box, so the mark stays optically centred in the band
    // at every size instead of resting on a fixed baseline.
    baseline: Math.round((BAND_HEIGHT + fontSize * CAP_HEIGHT_RATIO) / 2),
    rows: Math.ceil(BAND_HEIGHT / STRIPE_STEP),
  }
}
