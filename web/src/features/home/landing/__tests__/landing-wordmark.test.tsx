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
import { describe, expect, test } from 'vitest'

import { computeWordmarkMetrics, estimateEmWidth } from '../wordmark-metrics'

describe('estimateEmWidth', () => {
  // Multi-character strings: a single character hits the floor below.
  test('charges less for a space than for a glyph', () => {
    expect(estimateEmWidth('aaa')).toBeGreaterThan(estimateEmWidth('a a'))
  })

  test('charges more for CJK than for latin', () => {
    expect(estimateEmWidth('接口')).toBeGreaterThan(estimateEmWidth('ab'))
  })

  test('never returns less than a floor, so an empty name cannot collapse', () => {
    expect(estimateEmWidth('')).toBeGreaterThan(0)
    expect(estimateEmWidth(' ')).toBeGreaterThan(0)
  })
})

describe('computeWordmarkMetrics', () => {
  // The regression this guards: the mark used to carry
  // textLength + lengthAdjust="spacingAndGlyphs", which stretched or squashed
  // the operator's site name by up to 2x depending on its length.
  test.each([
    'New API',
    '接',
    'A',
    '一个非常非常长的中文站点名称',
    'x'.repeat(40),
  ])('never lets %s overflow the band it is clipped against', (brand) => {
    const em = estimateEmWidth(brand)
    const { fontSize, width } = computeWordmarkMetrics(brand)

    expect(width).toBeGreaterThan(0)
    expect(fontSize).toBeGreaterThan(0)
    expect(em * fontSize).toBeLessThanOrEqual(width)
  })

  test('keeps the band at its nominal width for a realistic name', () => {
    expect(computeWordmarkMetrics('New API').width).toBe(1120)
    expect(computeWordmarkMetrics('接口').width).toBe(1120)
  })

  test('widens the band instead of clipping a very long name', () => {
    // Past MIN_FONT_SIZE the natural width no longer fits, and the old
    // textLength version cut the ends off.
    expect(computeWordmarkMetrics('x'.repeat(40)).width).toBeGreaterThan(1120)
  })

  test('keeps the baseline inside the viewBox so the mark is not clipped', () => {
    for (const brand of ['New API', '接', 'x'.repeat(40)]) {
      const { baseline, height } = computeWordmarkMetrics(brand)

      expect(baseline).toBeGreaterThan(0)
      expect(baseline).toBeLessThan(height)
    }
  })

  test('draws enough stripes to cover the band', () => {
    for (const brand of ['New API', '接', 'x'.repeat(40)]) {
      const { height, rows } = computeWordmarkMetrics(brand)

      expect(rows * 6).toBeGreaterThanOrEqual(height)
    }
  })
})
