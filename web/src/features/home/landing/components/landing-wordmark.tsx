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
import type { CSSProperties } from 'react'

import { useVisibleMotion } from '@/hooks/use-visible-motion'

import {
  computeWordmarkMetrics,
  STRIPE_STEP,
  STRIPE_THICKNESS,
} from '../wordmark-metrics'

/**
 * Single instance per page, so a constant id is enough — and unlike
 * `useId()` it stays pure ASCII, which keeps it valid inside `url(#…)`.
 */
const CLIP_ID = 'landing-wordmark-clip'

/**
 * The site name, clipped into a stack of thin stripes that a slow sweep fills.
 */
export function LandingWordmark({ brand }: { brand: string }) {
  // The stripe sweep is the one decoration on the page that animates forever,
  // so it runs only while the mark is on screen and the tab is in front.
  const { ref, playing } = useVisibleMotion<HTMLDivElement>()
  const { fontSize, width, height, baseline, rows } =
    computeWordmarkMetrics(brand)

  return (
    <div
      ref={ref}
      className='landing-wordmark'
      data-playing={playing}
      aria-hidden='true'
    >
      <svg viewBox={`0 0 ${width} ${height}`} focusable='false'>
        <defs>
          <clipPath id={CLIP_ID}>
            <text
              x={width / 2}
              y={baseline}
              fontSize={fontSize}
              textAnchor='middle'
            >
              {brand}
            </text>
          </clipPath>
        </defs>
        <g clipPath={`url(#${CLIP_ID})`}>
          {Array.from({ length: rows }, (_, row) => (
            <rect
              key={row}
              x='0'
              y={row * STRIPE_STEP}
              width={width}
              height={STRIPE_THICKNESS}
              rx='1'
              style={{ '--stripe-row': row } as CSSProperties}
            />
          ))}
        </g>
      </svg>
    </div>
  )
}
