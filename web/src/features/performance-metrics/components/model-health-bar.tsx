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
import { useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import { getSuccessRateLevel } from '../lib/format'
import type { SuccessRatePoint } from '../types'

type ModelHealthBarProps = {
  successRate?: number
  windowStart?: number
  points?: SuccessRatePoint[]
  compact?: boolean
}

export function ModelHealthBar(props: ModelHealthBarProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.language)
  const [active, setActive] = useState(23)
  const buttons = useRef<(HTMLButtonElement | null)[]>([])
  const level = getSuccessRateLevel(props.successRate ?? Number.NaN)
  const rateFormat = useMemo(
    () =>
      new Intl.NumberFormat(locale, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
      }),
    [locale]
  )
  const dateFormat = useMemo(
    () =>
      new Intl.DateTimeFormat(locale, {
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        hourCycle: 'h23',
      }),
    [locale]
  )
  const hours = useMemo(() => {
    const start = props.windowStart
    const validWindow =
      start != null &&
      Number.isFinite(start) &&
      start >= 0 &&
      start < 8.64e12 - 86400
    const rates = new Map(
      props.points?.map((point) => [point.ts, point.success_rate])
    )
    return Array.from({ length: 24 }, (_, index) => {
      const ts = validWindow ? start + index * 3600 : undefined
      const rate = ts == null ? undefined : rates.get(ts)
      return {
        id: ts ?? `hour-${index}`,
        ts,
        rate,
        level: getSuccessRateLevel(rate ?? Number.NaN),
      }
    })
  }, [props.windowStart, props.points])

  return (
    <div
      className={cn('model-health', props.compact && 'model-health-compact')}
    >
      <div className='model-health-heading'>
        <span
          title={t(
            'Success rate excludes business rejections and includes the current partial hour.'
          )}
        >
          <span>{t('Success rate')}</span>{' '}
          <span className='model-health-period'>24h</span>
        </span>
        <span data-level={level}>
          {level === 'unknown'
            ? '—'
            : `${rateFormat.format(props.successRate ?? 0)}%`}
        </span>
      </div>
      <div
        className='model-health-bars gap-px'
        role='group'
        aria-label={t('Hourly model health')}
        onKeyDown={(event) => {
          let next = active
          if (event.key === 'ArrowLeft') next = Math.max(0, active - 1)
          else if (event.key === 'ArrowRight') next = Math.min(23, active + 1)
          else if (event.key === 'Home') next = 0
          else if (event.key === 'End') next = 23
          else return
          event.preventDefault()
          event.stopPropagation()
          setActive(next)
          buttons.current[next]?.focus()
        }}
      >
        {hours.map((hour, index) => {
          const time =
            hour.ts == null
              ? t('Hour {{hour}}', { hour: index + 1 })
              : dateFormat.format(new Date(hour.ts * 1000))
          const value =
            hour.level === 'unknown'
              ? t('No data')
              : `${rateFormat.format(hour.rate ?? 0)}%`
          const label = `${time} · ${value}`
          return (
            <Tooltip key={hour.id}>
              <TooltipTrigger
                render={
                  <button
                    type='button'
                    ref={(element) => {
                      buttons.current[index] = element
                    }}
                    tabIndex={index === active ? 0 : -1}
                    className='model-health-hour'
                    data-level={hour.level}
                    aria-label={label}
                    onFocus={() => setActive(index)}
                    onClick={(event) => event.stopPropagation()}
                  />
                }
              />
              <TooltipContent>
                <div>
                  <p>{time}</p>
                  <p>
                    {hour.level === 'unknown'
                      ? t('No data')
                      : `${t('Success rate')}: ${value}`}
                  </p>
                </div>
              </TooltipContent>
            </Tooltip>
          )
        })}
      </div>
      {!props.compact && hours[0].ts != null && (
        <div className='model-health-axis'>
          {[hours[0], hours[23]].map((hour) => (
            <span key={hour.id}>
              {hour.ts != null && (
                <time dateTime={new Date(hour.ts * 1000).toISOString()}>
                  {dateFormat.format(new Date(hour.ts * 1000))}
                </time>
              )}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
