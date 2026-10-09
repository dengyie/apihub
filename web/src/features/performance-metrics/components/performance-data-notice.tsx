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
import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { formatTimestampToDate } from '@/lib/format'

interface PerformanceDataNoticeProps {
  error: unknown
  updatedAt: number
  onRetry: () => Promise<unknown>
}

/** Keep request failures distinct from empty metrics and cached measurements. */
export function PerformanceDataNotice(props: PerformanceDataNoticeProps) {
  const { t } = useTranslation()
  if (!props.error) return null
  const retry = () => {
    void props.onRetry()
  }

  if (!props.updatedAt) {
    return (
      <ErrorState
        className='min-h-36'
        title={t('Failed to load performance data')}
        onRetry={retry}
      />
    )
  }

  return (
    <Alert variant='destructive'>
      <AlertTriangle />
      <AlertTitle>{t('Failed to load performance data')}</AlertTitle>
      <AlertDescription className='space-y-2'>
        <p>{t('Refresh failed. Showing the last successful snapshot.')}</p>
        <p>
          {t('Last updated')}:{' '}
          <time dateTime={new Date(props.updatedAt).toISOString()}>
            {formatTimestampToDate(props.updatedAt, 'milliseconds')}
          </time>
        </p>
        <Button size='sm' variant='outline' onClick={retry}>
          {t('Retry')}
        </Button>
      </AlertDescription>
    </Alert>
  )
}
