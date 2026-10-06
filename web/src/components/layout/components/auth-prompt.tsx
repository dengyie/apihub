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

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import type { AuthPrompt } from '@/hooks/use-auth-prompt'

/** Renders the countdown dialog owned by a {@link useAuthPrompt} instance. */
export function AuthPromptDialog({ prompt }: { prompt: AuthPrompt }) {
  const { t } = useTranslation()

  return (
    <Dialog
      open={!!prompt.target}
      onOpenChange={(open) => {
        if (!open) prompt.close()
      }}
      title={t('Sign in required')}
      description={t('Please sign in to view {{module}}.', {
        module: prompt.target?.title || '',
      })}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      footer={
        <>
          <Button variant='outline' onClick={prompt.close}>
            {t('Cancel')}
          </Button>
          <Button onClick={prompt.goToSignIn}>{t('Sign in now')}</Button>
        </>
      }
    >
      <div className='bg-muted/40 text-muted-foreground rounded-lg px-3 py-2 text-sm'>
        {t('Redirecting to sign in in {{seconds}} seconds.', {
          seconds: prompt.secondsLeft,
        })}
      </div>
    </Dialog>
  )
}
