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
import { LogOut } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { SignOutDialog } from '@/components/sign-out-dialog'
import { ThemeSwitch } from '@/components/theme-switch'
import { Button } from '@/components/ui/button'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

export function SidebarSignOutButton() {
  const { t } = useTranslation()
  const [dialogOpen, setDialogOpen] = useState(false)
  const user = useAuthStore((state) => state.auth.user)
  // Currency formatters read this same store; subscribe so a currency change
  // updates the balance without waiting for another account request.
  useSystemConfigStore((state) => state.config.currency)

  return (
    <>
      <div className='snowapi-astryx-footer'>
        {user && <ProfileDropdown presentation='sidebar' />}
        {user?.quota != null && Number.isFinite(user.quota) && (
          <div className='snowapi-sidebar-balance'>
            <span>{t('Available balance')}</span>
            <strong>{formatQuotaWithCurrency(user.quota)}</strong>
          </div>
        )}
        <div className='snowapi-sidebar-tools'>
          <ThemeSwitch />
          <LanguageSwitcher />
          <Button
            type='button'
            variant='ghost'
            className='snowapi-sidebar-sign-out'
            aria-label={t('Sign out')}
            title={t('Sign out')}
            onClick={() => setDialogOpen(true)}
          >
            <LogOut className='size-4 shrink-0' aria-hidden='true' />
            <span className='sr-only'>{t('Sign out')}</span>
          </Button>
        </div>
      </div>
      <SignOutDialog open={dialogOpen} onOpenChange={setDialogOpen} />
    </>
  )
}
