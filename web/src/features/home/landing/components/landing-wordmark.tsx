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

import { useLandingInView } from '../hooks'

const ROWS = Array.from({ length: 36 }, (_, row) => row)

/**
 * The site name, clipped into a stack of thin stripes that a slow sweep fills.
 *
 * `textLength` makes the mark fit the viewBox whatever the operator called
 * their deployment, so a two-word name stretches the same way a short one does.
 */
export function LandingWordmark({ brand }: { brand: string }) {
  const clipId = useId()
  const [ref, inView] = useLandingInView<HTMLDivElement>()

  return (
    <div
      ref={ref}
      className='landing-wordmark'
      data-playing={inView}
      aria-hidden='true'
    >
      <svg viewBox='0 0 1120 210' focusable='false'>
        <defs>
          <clipPath id={clipId}>
            <text
              x='0'
              y='205'
              textLength='1120'
              lengthAdjust='spacingAndGlyphs'
            >
              {brand}
            </text>
          </clipPath>
        </defs>
        <g clipPath={`url(#${clipId})`}>
          {ROWS.map((row) => (
            <rect
              key={row}
              x='0'
              y={row * 6}
              width='1120'
              height='2'
              rx='1'
              style={{ '--stripe-row': row } as CSSProperties}
            />
          ))}
        </g>
      </svg>
    </div>
  )
}
