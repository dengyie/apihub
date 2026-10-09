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
import { useId, type CSSProperties } from 'react'

import { useVisibleMotion } from '@/hooks/use-visible-motion'

import {
  computeWordmarkMetrics,
  STRIPE_STEP,
  STRIPE_THICKNESS,
} from '../wordmark-metrics'

/**
 * The site name, clipped into a stack of thin stripes that a slow sweep fills.
 */
export function LandingWordmark(props: { brand: string; paused?: boolean }) {
  const clipId = `landing-wordmark-${useId()}`
  // Run each sweep only while its mark is visible and motion is enabled.
  const { ref, playing } = useVisibleMotion<HTMLSpanElement>(props.paused)
  const { fontSize, width, height, baseline, rows } = computeWordmarkMetrics(
    props.brand
  )
  const stripes = Array.from({ length: rows }, (_, row) => (
    <rect
      key={row}
      x='0'
      y={row * STRIPE_STEP}
      width={width}
      height={STRIPE_THICKNESS}
      rx='1'
      style={{ '--stripe-row': row } as CSSProperties}
    />
  ))

  return (
    <span
      ref={ref}
      className='landing-wordmark'
      data-playing={playing}
      aria-hidden='true'
    >
      <svg viewBox={`0 0 ${width} ${height}`} focusable='false'>
        <defs>
          <clipPath id={clipId}>
            <text
              x={width / 2}
              y={baseline}
              fontSize={fontSize}
              textAnchor='middle'
            >
              {props.brand}
            </text>
          </clipPath>
        </defs>
        <g clipPath={`url(#${clipId})`}>
          <g className='landing-wordmark-base'>{stripes}</g>
          <g className='landing-wordmark-sweep'>{stripes}</g>
        </g>
      </svg>
    </span>
  )
}
