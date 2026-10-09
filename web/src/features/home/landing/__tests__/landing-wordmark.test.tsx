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
import { cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { LandingWordmark } from '../components/landing-wordmark'
import {
  computeWordmarkMetrics,
  estimateEmWidth,
  WORDMARK_DESCENT_RATIO,
} from '../wordmark-metrics'

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
    'MangoApi',
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
    expect(computeWordmarkMetrics('MangoApi').width).toBe(1120)
    expect(computeWordmarkMetrics('接口').width).toBe(1120)
  })

  // The regression this guards: the band used to widen past MIN_FONT_SIZE while
  // its height shrank with the font size, and since the SVG is `height: auto`
  // the whole footer collapsed into a sliver for a long site name.
  test.each(['MangoApi', '一个非常非常长的中文站点名称', 'x'.repeat(40)])(
    'holds the box at a constant %s size whatever the name length',
    (brand) => {
      const reference = computeWordmarkMetrics('MangoApi')

      expect(computeWordmarkMetrics(brand)).toMatchObject({
        width: reference.width,
        height: reference.height,
        rows: reference.rows,
      })
    }
  )

  test('keeps the baseline inside the viewBox so the mark is not clipped', () => {
    for (const brand of ['MangoApi', '接', 'x'.repeat(40)]) {
      const { baseline, fontSize, height } = computeWordmarkMetrics(brand)

      expect(baseline).toBeGreaterThan(0)
      expect(baseline).toBeLessThan(height)
      expect(baseline + fontSize * WORDMARK_DESCENT_RATIO).toBeLessThanOrEqual(
        height
      )
    }
  })

  test('draws enough stripes to cover the band', () => {
    for (const brand of ['MangoApi', '接', 'x'.repeat(40)]) {
      const { height, rows } = computeWordmarkMetrics(brand)

      expect(rows * 6).toBeGreaterThanOrEqual(height)
    }
  })
})

describe('wordmark rendering and motion', () => {
  beforeEach(() => {
    const matchMedia = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...matchMedia(query),
      matches: false,
    }))
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(private callback: IntersectionObserverCallback) {}
        observe(target: Element) {
          this.callback(
            [{ isIntersecting: true, target } as IntersectionObserverEntry],
            this as unknown as IntersectionObserver
          )
        }
        disconnect() {}
      }
    )
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  test('each mark clips its own name when the hero and footer render together', () => {
    const { container, rerender } = render(
      <>
        <LandingWordmark brand='MangoApi' />
        <LandingWordmark brand='Footer brand' />
      </>
    )
    const clips = container.querySelectorAll('clipPath')
    const marks = container.querySelectorAll('svg > g')
    expect(clips).toHaveLength(2)
    expect(clips[0].id).not.toBe(clips[1].id)
    expect(marks[0]).toHaveAttribute('clip-path', `url(#${clips[0].id})`)
    expect(marks[1]).toHaveAttribute('clip-path', `url(#${clips[1].id})`)
    const firstClipId = clips[0].id

    rerender(
      <>
        <LandingWordmark brand='Updated MangoApi' />
        <LandingWordmark brand='Footer brand' />
      </>
    )
    expect(container.querySelector('clipPath')).toHaveAttribute(
      'id',
      firstClipId
    )
    expect(clips[0]).toHaveTextContent('Updated MangoApi')
    expect(clips[1]).toHaveTextContent('Footer brand')
  })

  test('pauses and resumes the visible stripe animation with the hero control', () => {
    const { container, rerender } = render(<LandingWordmark brand='MangoApi' />)
    const mark = container.querySelector('.landing-wordmark')
    expect(mark).toHaveAttribute('data-playing', 'true')

    rerender(<LandingWordmark brand='MangoApi' paused />)
    expect(mark).toHaveAttribute('data-playing', 'false')

    rerender(<LandingWordmark brand='MangoApi' paused={false} />)
    expect(mark).toHaveAttribute('data-playing', 'true')
  })

  test('keeps the brand visible and static when reduced motion is preferred', () => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    vi.mocked(window.matchMedia).mockReturnValue({ ...media, matches: true })
    const { container } = render(<LandingWordmark brand='MangoApi' />)
    expect(container.querySelector('.landing-wordmark')).toHaveAttribute(
      'data-playing',
      'false'
    )
    expect(container.querySelector('clipPath text')).toHaveTextContent(
      'MangoApi'
    )
  })
})
