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
import type { Table } from '@tanstack/react-table'
import { Database } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

import { LOG_TYPE_ENUM } from '../constants'
import type { UsageLog } from '../data/schema'
import type { LogCategory } from '../types'
import { CommonLogMobileCard } from './common-log-mobile-card'

const logTypeRowTint: Record<number, string> = {
  [LOG_TYPE_ENUM.ERROR]:
    'bg-rose-50/40 dark:bg-rose-950/20 border-rose-200/50 dark:border-rose-900/30',
  [LOG_TYPE_ENUM.REFUND]:
    'bg-blue-50/30 dark:bg-blue-950/15 border-blue-200/50 dark:border-blue-900/30',
}

interface UsageLogsMobileListProps<TData> {
  table: Table<TData>
  isLoading?: boolean
  emptyTitle?: string
  emptyDescription?: string
  logCategory: LogCategory
}

function UsageLogsMobileSkeleton(props: { separate: boolean }) {
  const { t } = useTranslation()
  return (
    <div
      role='status'
      aria-label={t('Loading')}
      aria-busy='true'
      className={
        props.separate
          ? 'min-w-0 space-y-3'
          : 'border-border/50 bg-card overflow-hidden rounded-lg border'
      }
    >
      {[1, 2, 3].map((i) => (
        <div
          key={i}
          className={
            props.separate
              ? 'border-border/60 bg-card space-y-3 rounded-xl border p-3.5'
              : 'border-border/40 space-y-2.5 border-b p-3 last:border-b-0'
          }
        >
          <div className='flex items-center justify-between gap-3'>
            <Skeleton className='h-5 w-40 rounded-md' />
            <Skeleton className='h-5 w-16 rounded-md' />
          </div>
          <div className='grid grid-cols-2 gap-x-4 gap-y-2'>
            {[1, 2, 3, 4, 5, 6].map((j) => (
              <div key={j} className='min-w-0 space-y-1'>
                <Skeleton className='h-3 w-10 rounded' />
                <Skeleton className='h-4 w-full rounded' />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

export function UsageLogsMobileList<TData>({
  table,
  isLoading = false,
  emptyTitle,
  emptyDescription,
  logCategory: _logCategory = 'common',
}: UsageLogsMobileListProps<TData>) {
  const { t } = useTranslation()

  const resolvedEmptyTitle = emptyTitle ?? t('No Logs Found')
  const resolvedEmptyDescription =
    emptyDescription ??
    t('No usage logs available. Logs will appear here once API calls are made.')

  if (isLoading) {
    return <UsageLogsMobileSkeleton separate />
  }

  const rows = table.getRowModel().rows

  if (!rows || rows.length === 0) {
    return (
      <div className='rounded-lg border p-6'>
        <Empty className='border-none p-0'>
          <EmptyHeader>
            <EmptyMedia variant='icon'>
              <Database className='size-6' />
            </EmptyMedia>
            <EmptyTitle>{resolvedEmptyTitle}</EmptyTitle>
            <EmptyDescription>{resolvedEmptyDescription}</EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>
    )
  }

  return (
    <div className='min-w-0 space-y-3'>
      {rows.map((row) => {
        const cells = new Map(
          row.getVisibleCells().map((cell) => [cell.column.id, cell])
        )

        const logType = (row.original as Record<string, unknown>).type as
          | number
          | undefined
        const tintClass = logType != null ? (logTypeRowTint[logType] ?? '') : ''

        return (
          <div
            key={row.id}
            className={cn(
              'border-border/60 bg-card min-w-0 rounded-xl border p-3.5',
              tintClass
            )}
          >
            <CommonLogMobileCard
              log={row.original as UsageLog}
              cells={cells}
            />
          </div>
        )
      })}
    </div>
  )
}
