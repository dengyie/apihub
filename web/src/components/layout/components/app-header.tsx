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
import { ConfigDrawer } from '@/components/config-drawer'
import { LanguageSwitcher } from '@/components/language-switcher'
import { NotificationPopover } from '@/components/notification-popover'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { Search } from '@/components/search'
import { SystemUpdateAction } from '@/features/system-update/system-update-action'
import { useNotifications } from '@/hooks/use-notifications'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'

import { Header } from './header'
import { NavigationDock } from './navigation-dock'
import { SystemBrand } from './system-brand'

/**
 * General application Header component
 * Integrates navigation bar, search, configuration and profile functions
 *
 * @example
 * // Basic usage
 * <AppHeader />
 *
 * @example
 * // Hide the search box
 * <AppHeader showSearch={false} />
 */
type AppHeaderProps = {
  /**
   * Whether to show search box
   * @default true
   */
  showSearch?: boolean
  /**
   * Whether to show notification button
   * @default true
   */
  showNotifications?: boolean
  /**
   * Whether to show config drawer
   * @default true
   */
  showConfigDrawer?: boolean
  /**
   * Whether to show profile dropdown
   * @default true
   */
  showProfileDropdown?: boolean
}

export function AppHeader({
  showSearch = true,
  showNotifications = true,
  showConfigDrawer = true,
  showProfileDropdown = true,
}: AppHeaderProps) {
  const links = useTopNavLinks()

  // Notifications hook
  const notifications = useNotifications()

  return (
    <>
      <Header>
        <div className='@container/system-brand flex min-w-0 flex-1 items-center gap-1'>
          <SystemBrand variant='inline' />
          <SystemUpdateAction presentation='version' />
        </div>

        <div className='ms-auto flex shrink-0 items-center gap-1 sm:gap-2'>
          {showSearch && (
            <Search className='w-8 flex-none [&>span]:hidden sm:[&>span]:inline' />
          )}
          {showNotifications && (
            <NotificationPopover
              open={notifications.popoverOpen}
              onOpenChange={notifications.setPopoverOpen}
              unreadCount={notifications.unreadCount}
              activeTab={notifications.activeTab}
              onTabChange={notifications.setActiveTab}
              notice={notifications.notice}
              announcements={notifications.announcements}
              loading={notifications.loading}
            />
          )}
          <LanguageSwitcher />
          {showConfigDrawer && <ConfigDrawer />}
          {showProfileDropdown && <ProfileDropdown />}
        </div>
      </Header>
      <NavigationDock links={links} activeTarget='console' />
    </>
  )
}
