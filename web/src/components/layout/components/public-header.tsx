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
import { Link, useRouterState } from '@tanstack/react-router'
import { ArrowUpRight, Menu, X } from 'lucide-react'
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import { NotificationPopover } from '@/components/notification-popover'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetFooter,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { SystemUpdateAction } from '@/features/system-update/system-update-action'
import { useAuthPrompt } from '@/hooks/use-auth-prompt'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useNotifications } from '@/hooks/use-notifications'
import { useSystemConfig } from '@/hooks/use-system-config'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import type { NavigationDockTarget } from '../lib/navigation-dock-items'
import type { TopNavLink } from '../types'
import { AuthPromptDialog } from './auth-prompt'
import { HeaderLogo } from './header-logo'
import { NavigationDock } from './navigation-dock'
import { PublicNavLinks } from './public-nav-links'

export interface PublicHeaderProps {
  showThemeSwitch?: boolean
  showLanguageSwitcher?: boolean
  logo?: React.ReactNode
  siteName?: string
  homeUrl?: string
  showAuthButtons?: boolean
  showNotifications?: boolean
  className?: string
}

export function PublicHeader(props: PublicHeaderProps) {
  const { t } = useTranslation()
  const [mobileOpen, setMobileOpen] = useState(false)
  const compact = useMediaQuery('(max-width: 1023px)')
  const authPrompt = useAuthPrompt()
  const user = useAuthStore((state) => state.auth.user)
  const { systemName, logo, loading, logoLoaded } = useSystemConfig()
  const links = useTopNavLinks()
  const notifications = useNotifications()
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })
  const brand = props.siteName || systemName
  const showAuth = props.showAuthButtons !== false
  const showTheme = props.showThemeSwitch !== false
  const showLanguage = props.showLanguageSwitcher !== false
  const showNotifications = props.showNotifications !== false

  let dockActiveTarget: NavigationDockTarget | undefined
  if (pathname === '/') {
    dockActiveTarget = 'home'
  } else if (pathname.startsWith('/models') || pathname === '/pricing') {
    dockActiveTarget = 'models'
  } else if (pathname.startsWith('/dashboard')) {
    dockActiveTarget = 'console'
  }

  let logoContent: ReactNode = props.logo ?? (
    <HeaderLogo
      src={logo}
      alt=''
      loading={loading}
      logoLoaded={logoLoaded}
      className='size-8 rounded-lg object-contain'
    />
  )
  if (loading) logoContent = <Skeleton className='size-8 rounded-lg' />
  let authContent = (
    <Button
      size='sm'
      className='h-9 rounded-full px-5'
      render={<Link to='/sign-in' />}
    >
      {t('Sign in')}
      <ArrowUpRight size={14} />
    </Button>
  )
  if (user) authContent = <ProfileDropdown />
  if (loading) authContent = <Skeleton className='h-9 w-20 rounded-full' />

  useEffect(() => {
    setMobileOpen(false)
  }, [pathname])
  useEffect(() => {
    if (!compact) setMobileOpen(false)
  }, [compact])
  const handleNavLinkClick = useCallback(
    (event: React.MouseEvent<HTMLAnchorElement>, link: TopNavLink) => {
      if (authPrompt.interceptLinkClick(event, link)) return
      setMobileOpen(false)
    },
    [authPrompt]
  )

  return (
    <>
      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <header
          data-slot='public-header'
          className={cn('site-public-header', props.className)}
        >
          <div className='site-public-bar'>
            <div className='site-public-brand'>
              <Link to={props.homeUrl ?? '/'}>
                {logoContent}
                <span title={brand}>
                  {loading ? <Skeleton className='h-5 w-20' /> : brand}
                </span>
              </Link>
              <SystemUpdateAction presentation='version' />
            </div>
            <nav className='site-public-desktop' aria-label={t('Navigation')}>
              <PublicNavLinks
                links={links}
                pathname={pathname}
                onLinkClick={handleNavLinkClick}
              />
            </nav>
            <div className='site-public-actions'>
              <div className='hidden items-center gap-1 lg:flex'>
                {showLanguage && <LanguageSwitcher />}
                {showTheme && <ThemeSwitch />}
              </div>
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
              {showAuth && <div className='hidden sm:block'>{authContent}</div>}
              <SheetTrigger
                render={
                  <Button
                    variant='ghost'
                    size='icon'
                    className='lg:hidden'
                    aria-label={t('Toggle navigation menu')}
                  />
                }
              >
                <Menu size={18} />
              </SheetTrigger>
            </div>
          </div>
        </header>
        <SheetContent
          side='right'
          showCloseButton={false}
          className='public-mobile-sheet'
        >
          <SheetHeader>
            <SheetTitle>{brand}</SheetTitle>
            <SheetClose
              render={
                <Button
                  variant='ghost'
                  size='icon'
                  className='absolute top-5 right-5'
                  aria-label={t('Close')}
                />
              }
            >
              <X size={18} />
            </SheetClose>
          </SheetHeader>
          <nav className='public-mobile-nav' aria-label={t('Navigation')}>
            <PublicNavLinks
              links={links}
              pathname={pathname}
              onLinkClick={handleNavLinkClick}
            />
          </nav>
          <SheetFooter>
            <div className='flex gap-2'>
              {showTheme && <ThemeSwitch />}
              {showLanguage && <LanguageSwitcher />}
            </div>
            {showAuth && (
              <Button
                className='rounded-full'
                render={
                  <Link
                    to={user ? '/dashboard' : '/sign-in'}
                    onClick={() => setMobileOpen(false)}
                  />
                }
              >
                {user ? t('Go to Dashboard') : t('Sign in')}
                <ArrowUpRight size={16} />
              </Button>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
      {!mobileOpen && (
        <NavigationDock
          links={links}
          activeTarget={dockActiveTarget}
          onLinkClick={handleNavLinkClick}
        />
      )}
      <AuthPromptDialog prompt={authPrompt} />
    </>
  )
}
