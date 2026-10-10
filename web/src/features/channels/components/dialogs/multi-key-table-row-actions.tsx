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
import { Globe } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import type { MultiKeyConfirmAction } from '../../types'

type MultiKeyTableRowActionsProps = {
  keyIndex: number
  status: number
  proxy?: string
  canDelete: boolean
  canEditSensitive?: boolean
  onAction: (action: MultiKeyConfirmAction) => void
  onEditProxy: (keyIndex: number, currentProxy?: string) => void
}

export function MultiKeyTableRowActions({
  keyIndex,
  status,
  proxy,
  canDelete,
  canEditSensitive,
  onAction,
  onEditProxy,
}: MultiKeyTableRowActionsProps) {
  const { t } = useTranslation()
  const isEnabled = status === 1
  const canEditProxy = canEditSensitive ?? canDelete

  return (
    <div className='flex justify-end gap-2'>
      <Button
        variant='outline'
        size='sm'
        onClick={() => {
          if (!canEditProxy) return
          onEditProxy(keyIndex, proxy)
        }}
        disabled={!canEditProxy}
        title={
          canEditProxy
            ? t('Configure independent egress proxy for this key')
            : t('No permission to perform this action')
        }
      >
        <Globe className='mr-1.5 h-3.5 w-3.5' />
        {t('Proxy')}
      </Button>
      {isEnabled ? (
        <Button
          variant='outline'
          size='sm'
          onClick={() => onAction({ type: 'disable', keyIndex })}
        >
          {t('Disable')}
        </Button>
      ) : (
        <Button
          variant='outline'
          size='sm'
          onClick={() => onAction({ type: 'enable', keyIndex })}
        >
          {t('Enable')}
        </Button>
      )}
      <Button
        variant='destructive'
        size='sm'
        onClick={() => {
          if (!canDelete) return
          onAction({ type: 'delete', keyIndex })
        }}
        disabled={!canDelete}
        title={
          canDelete ? undefined : t('No permission to perform this action')
        }
      >
        {t('Delete')}
      </Button>
    </div>
  )
}
