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
import { AppShell, useAppShellMobile } from '@astryxdesign/core/AppShell'
import { InternationalizationProvider } from '@astryxdesign/core/i18n'
import { LinkProvider } from '@astryxdesign/core/Link'
import { SideNavRenderContext } from '@astryxdesign/core/SideNav'
import { Cancel01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useIsFetching } from '@tanstack/react-query'
import { Link, type LinkProps } from '@tanstack/react-router'
import { forwardRef, useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { ContentLoading } from '@/components/content-loading'
import { AnimatedOutlet } from '@/components/page-transition'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetTitle,
} from '@/components/ui/sheet'
import { useDirection } from '@/context/direction-provider'
import { useSystemConfig } from '@/hooks/use-system-config'
import { toIntlLocale } from '@/i18n/languages'
import { DEFAULT_LOGO } from '@/lib/constants'

import { AstryxNavigation } from './astryx-navigation'
import { SidebarSignOutButton } from './sidebar-sign-out-button'

type AstryxAppShellProps = {
  children?: React.ReactNode
}

type SnowMobileNavigationProps = {
  logo: string
}

const TanStackLinkAdapter = forwardRef<
  HTMLAnchorElement,
  React.ComponentProps<typeof Link> & { href?: string }
>(({ href, to, ...props }, ref) => {
  return (
    <Link
      ref={ref}
      to={(to ?? href) as unknown as LinkProps['to']}
      {...props}
    />
  )
})
TanStackLinkAdapter.displayName = 'TanStackLinkAdapter'

function SnowMobileNavigation({ logo }: SnowMobileNavigationProps) {
  const { t } = useTranslation()
  const { isMobileNavOpen, closeMobileNav, mobileNavId } = useAppShellMobile()

  return (
    <Sheet
      open={isMobileNavOpen}
      onOpenChange={(open) => {
        if (!open) closeMobileNav()
      }}
    >
      <SheetContent
        id={mobileNavId}
        side='right'
        showCloseButton={false}
        overlayClassName='snowapi-mobile-sheet-overlay'
        className='snowapi-mobile-sheet w-[min(374px,100vw)] max-w-none gap-0 border-l duration-[240ms] ease-[cubic-bezier(0.22,1,0.36,1)] sm:max-w-none'
        aria-label={t('Navigation')}
        onClick={(event) => {
          if ((event.target as HTMLElement).closest('a')) closeMobileNav()
        }}
      >
        <SheetTitle className='sr-only'>{t('Navigation')}</SheetTitle>
        <div className='flex h-12 shrink-0 items-center justify-between border-b px-2'>
          <Link
            to='/dashboard'
            className='snowapi-astryx-logo-link'
            aria-label='SnowAPI'
          >
            <img src={logo || DEFAULT_LOGO} alt='' width={24} height={24} />
          </Link>
          <SheetClose
            render={
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Close navigation')}
              />
            }
          >
            <HugeiconsIcon icon={Cancel01Icon} strokeWidth={2} />
          </SheetClose>
        </div>
        <div className='min-h-0 flex-1 overflow-y-auto p-2'>
          <SideNavRenderContext value='drawer-content'>
            <AstryxNavigation
              logo={logo}
              footer={
                <div className='snowapi-mobile-nav-footer'>
                  <SidebarSignOutButton />
                </div>
              }
            />
          </SideNavRenderContext>
        </div>
      </SheetContent>
    </Sheet>
  )
}

export function AstryxAppShell(props: AstryxAppShellProps) {
  const { i18n, t } = useTranslation()
  const { dir } = useDirection()
  const { logo } = useSystemConfig()
  const initialQueryFetches = useIsFetching({
    predicate: (query) =>
      query.state.fetchStatus === 'fetching' && query.state.data === undefined,
  })
  const shellRef = useRef<HTMLDivElement>(null)
  const locale = toIntlLocale(i18n.resolvedLanguage ?? i18n.language) ?? 'en'

  const astryxOverrides = useMemo(
    () => ({
      [locale]: {
        '@astryx.appShell.mobileNavigation': t('Mobile navigation'),
        '@astryx.mobileNav.closeNavigation': t('Close navigation'),
        '@astryx.mobileNav.navigation': t('Navigation'),
        '@astryx.mobileNav.toggle.open': t('Open navigation'),
        '@astryx.sideNav.label': t('Navigation'),
        '@astryx.sideNavCollapseButton.collapseSidebar': t('Collapse sidebar'),
        '@astryx.sideNavCollapseButton.expandSidebar': t('Expand sidebar'),
        '@astryx.sideNavItem.collapse': t('Collapse {label}'),
        '@astryx.sideNavItem.expand': t('Expand {label}'),
      },
    }),
    [locale, t]
  )

  useEffect(() => {
    const skipLink = shellRef.current?.querySelector<HTMLAnchorElement>(
      '[data-testid="skip-to-content"]'
    )
    if (skipLink) skipLink.textContent = t('Skip to Main')
  }, [i18n.resolvedLanguage, t])

  return (
    <InternationalizationProvider
      locale={locale}
      dir={dir}
      overrides={astryxOverrides}
    >
      <LinkProvider component={TanStackLinkAdapter}>
        <AppShell
          ref={shellRef}
          className='snowapi-astryx-shell'
          data-visual-region='console-shell'
          variant='surface'
          height='fill'
          contentPadding={0}
          mobileNav={{
            breakpoint: 'md',
            content: <SnowMobileNavigation logo={logo} />,
          }}
          sideNav={
            <AstryxNavigation logo={logo} footer={<SidebarSignOutButton />} />
          }
        >
          <div
            data-visual-region='content-frame'
            className='snowapi-astryx-content @container/content'
          >
            <div
              className='snowapi-console-content-state'
              data-loading={initialQueryFetches > 0 || undefined}
            >
              {initialQueryFetches > 0 ? (
                <ContentLoading className='snowapi-console-loading-indicator absolute inset-0 z-10 min-h-0' />
              ) : null}
              <div className='snowapi-console-loaded-content flex min-h-0 flex-1 flex-col'>
                {props.children ?? <AnimatedOutlet />}
              </div>
            </div>
          </div>
        </AppShell>
      </LinkProvider>
    </InternationalizationProvider>
  )
}
