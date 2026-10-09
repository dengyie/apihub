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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'

import { ModelHealthBar } from '../components/model-health-bar'

describe('hourly model health', () => {
  const start = 1_770_000_000
  it('labels a cached server window with its actual times instead of the current hour', () => {
    const { container } = render(
      <ModelHealthBar windowStart={Date.parse('2026-09-14T12:00:00Z') / 1000} />
    )
    expect(screen.queryByText('Current hour')).not.toBeInTheDocument()
    expect(
      Array.from(
        container.querySelectorAll('.model-health-axis time'),
        (element) => element.getAttribute('dateTime')
      )
    ).toEqual(['2026-09-14T12:00:00.000Z', '2026-09-15T11:00:00.000Z'])
  })
  it('distinguishes missing data, a failed hour, and the four health levels', () => {
    render(
      <ModelHealthBar
        successRate={97.12}
        windowStart={start}
        points={[
          { ts: start, success_rate: 0 },
          { ts: start + 3600, success_rate: 99 },
          { ts: start + 7200, success_rate: 95 },
          { ts: start + 10800, success_rate: 90 },
          { ts: start + 14400, success_rate: 101 },
        ]}
      />
    )
    const bars = within(screen.getByRole('group')).getAllByRole('button')
    expect(bars).toHaveLength(24)
    expect(
      bars.slice(0, 6).map((bar) => bar.getAttribute('data-level'))
    ).toEqual([
      'critical',
      'excellent',
      'good',
      'warning',
      'unknown',
      'unknown',
    ])
    expect(bars[0]).toHaveAccessibleName(/0.00%/)
    expect(bars[5]).toHaveAccessibleName(/No data/)
    expect(screen.getByText('97.12%')).toBeVisible()
  })
  it('offers one tab stop per model and arrow-key access to every hour', async () => {
    const user = userEvent.setup()
    render(<ModelHealthBar windowStart={start} />)
    const bars = within(screen.getByRole('group')).getAllByRole('button')
    expect(bars.filter((bar) => bar.tabIndex === 0)).toHaveLength(1)
    await user.tab()
    expect(bars[23]).toHaveFocus()
    await user.keyboard('{ArrowLeft}')
    expect(bars[22]).toHaveFocus()
    await user.keyboard('{Home}')
    expect(bars[0]).toHaveFocus()
    await user.keyboard('{End}')
    expect(bars[23]).toHaveFocus()
  })
  it('describes an unavailable hour as unknown without claiming no requests were made', async () => {
    const user = userEvent.setup()
    render(
      <TooltipProvider>
        <ModelHealthBar />
      </TooltipProvider>
    )
    await user.hover(screen.getAllByRole('button')[23])
    const tooltip = await screen.findByText(
      /^(No data|No requests recorded in this hour)$/
    )
    expect(tooltip).toHaveTextContent(/^No data$/)
  })
  it.each([Number.NaN, Infinity, -1, 9e12])(
    'treats invalid window %s as missing data',
    (windowStart) => {
      render(
        <ModelHealthBar
          windowStart={windowStart}
          points={[{ ts: windowStart, success_rate: 100 }]}
        />
      )
      expect(
        within(screen.getByRole('group'))
          .getAllByRole('button')
          .every((bar) => bar.getAttribute('data-level') === 'unknown')
      ).toBe(true)
    }
  )
})
