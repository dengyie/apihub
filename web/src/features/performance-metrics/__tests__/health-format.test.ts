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
import { describe, expect, it } from 'vitest'

import { formatUptimePct, getSuccessRateLevel } from '../lib/format'

describe('documented model health thresholds', () => {
  it.each([
    [100, 'excellent'],
    [99, 'excellent'],
    [98.99, 'good'],
    [95, 'good'],
    [94.99, 'warning'],
    [90, 'warning'],
    [89.99, 'critical'],
    [0, 'critical'],
    [Number.NaN, 'unknown'],
    [Number.POSITIVE_INFINITY, 'unknown'],
    [-1, 'unknown'],
    [101, 'unknown'],
  ])('classifies a success rate of %s as %s', (rate, level) => {
    expect(getSuccessRateLevel(rate)).toBe(level)
  })

  it.each([-1, 101, Number.NaN])(
    'does not display an invalid success rate of %s as a measurement',
    (rate) => {
      expect(formatUptimePct(rate)).toBe('—')
    }
  )
})
