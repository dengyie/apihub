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
import { memo } from 'react'
import { useTranslation } from 'react-i18next'

import { ModelHealthBar } from '@/features/performance-metrics/components/model-health-bar'
import {
  formatLatency,
  formatThroughput,
} from '@/features/performance-metrics/lib/format'
import type { SuccessRatePoint } from '@/features/performance-metrics/types'
import { cn } from '@/lib/utils'

export type ModelPerfBadgeData = {
  window_start?: number
  window_end?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_series?: SuccessRatePoint[]
}

export interface ModelPerfBadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  perf: ModelPerfBadgeData | undefined
}

export const ModelPerfBadge = memo(function ModelPerfBadge(
  props: ModelPerfBadgeProps
) {
  const { t } = useTranslation()
  const latency = formatLatency(props.perf?.avg_latency_ms ?? 0)
  const throughput = formatThroughput(props.perf?.avg_tps ?? 0).replace(
    ' t/s',
    't/s'
  )
  return (
    <div
      aria-label={t('Performance metrics for the last 24 hours')}
      className={cn('model-perf-badge', props.className)}
    >
      <ModelHealthBar
        successRate={props.perf?.success_rate}
        windowStart={props.perf?.window_start}
        points={props.perf?.recent_success_series}
      />
      <div className='model-perf-details'>
        <dl>
          <div title={t('Average latency')}>
            <dt>{t('Latency short')}</dt>
            <dd>{latency === '—' ? '—s' : latency}</dd>
          </div>
          <div title={t('Throughput')}>
            <dt>{t('Throughput short')}</dt>
            <dd>{throughput === '—' ? '—t/s' : throughput}</dd>
          </div>
        </dl>
        {props.children}
      </div>
    </div>
  )
})
