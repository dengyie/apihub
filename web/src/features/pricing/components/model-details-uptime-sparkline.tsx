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
import { ModelHealthBar } from '@/features/performance-metrics/components/model-health-bar'
import { cn } from '@/lib/utils'

import type { SuccessRateTimePoint } from '../lib/performance-chart-types'

// Hourly request success rates use the shared server-window health bar.

type SparklineSize = 'sm' | 'md'

type UptimeSparklineProps = {
  series: SuccessRateTimePoint[]
  windowStart?: number
  overallSuccessRate?: number
  size?: SparklineSize
  className?: string
}

export function UptimeSparkline(props: UptimeSparklineProps) {
  return (
    <div className={cn('min-w-36', props.className)}>
      <ModelHealthBar
        compact={props.size === 'sm'}
        successRate={props.overallSuccessRate}
        windowStart={props.windowStart}
        points={props.series.map((point) => ({
          ts: new Date(point.date).getTime() / 1000,
          success_rate: point.success_rate,
        }))}
      />
    </div>
  )
}
