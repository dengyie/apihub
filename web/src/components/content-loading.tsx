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
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'

export function ContentLoading(props: {
  className?: string
  variant?: 'spinner' | 'page'
}) {
  const { t } = useTranslation()

  if (props.variant === 'page') {
    return (
      <div
        className={props.className}
        role='status'
        aria-label={t('Loading page')}
      >
        <span className='sr-only'>{t('Loading page')}</span>
        <div aria-hidden='true' className='space-y-6 p-6 sm:p-8'>
          <Skeleton className='h-7 w-40' />
          <div className='grid grid-cols-2 gap-4 lg:grid-cols-4'>
            {['balance', 'requests', 'tokens', 'latency'].map((key) => (
              <Skeleton key={key} className='h-28 rounded-xl' />
            ))}
          </div>
          <Skeleton className='h-64 w-full rounded-xl' />
        </div>
      </div>
    )
  }

  return (
    <div
      className={cn(
        'flex min-h-52 w-full items-center justify-center',
        props.className
      )}
      role='status'
      aria-live='polite'
    >
      <div className='animate-spin motion-reduce:animate-none'>
        <Spinner className='size-6 animate-none' aria-hidden='true' />
      </div>
      <span className='sr-only'>{t('Loading...')}</span>
    </div>
  )
}

export function ContentReveal(props: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn('snowapi-content-reveal', props.className)}>
      {props.children}
    </div>
  )
}
