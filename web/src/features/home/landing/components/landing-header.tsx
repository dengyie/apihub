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
import { Link } from '@tanstack/react-router'
import { ArrowUpRight, Menu, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import { AuthPromptDialog } from '@/components/layout/components/auth-prompt'
import { PublicNavLinks } from '@/components/layout/components/public-nav-links'
import type { TopNavLink } from '@/components/layout/types'
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
import { useAuthPrompt } from '@/hooks/use-auth-prompt'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'

export function LandingHeader(props: {
  brand: string
  isAuthenticated: boolean
  logo?: string
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const compact = useMediaQuery('(max-width: 1023px)')
  const links = useTopNavLinks()
  const authPrompt = useAuthPrompt()
  useEffect(() => {
    if (!compact) setOpen(false)
  }, [compact])
  const onLinkClick = (
    event: React.MouseEvent<HTMLAnchorElement>,
    link: TopNavLink
  ) => {
    if (authPrompt.interceptLinkClick(event, link)) return
    setOpen(false)
  }
  const destination = props.isAuthenticated ? '/dashboard' : '/sign-in'
  const cta = props.isAuthenticated ? t('Go to Dashboard') : t('Sign in')

  return (
    <>
      <Sheet open={open} onOpenChange={setOpen}>
        <header className='landing-header'>
          <Link to='/' className='landing-brand'>
            {props.logo && <img src={props.logo} alt='' />}
            <span>{props.brand}</span>
          </Link>
          <nav className='landing-nav' aria-label={t('Navigation')}>
            <div className='landing-nav-links'>
              <PublicNavLinks links={links} onLinkClick={onLinkClick} />
            </div>
            <div className='landing-preferences'>
              <ThemeSwitch />
              <LanguageSwitcher />
            </div>
            <Link
              to={destination}
              className='landing-button landing-button-primary'
            >
              {cta}
              <ArrowUpRight size={14} aria-hidden='true' />
            </Link>
          </nav>
          <SheetTrigger className='landing-menu-toggle'>
            {t('Menu')}
            <Menu size={16} />
          </SheetTrigger>
        </header>
        <SheetContent
          side='right'
          showCloseButton={false}
          className='public-mobile-sheet'
        >
          <SheetHeader>
            <SheetTitle>{props.brand}</SheetTitle>
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
            <PublicNavLinks links={links} onLinkClick={onLinkClick} />
          </nav>
          <SheetFooter>
            <div className='landing-preferences'>
              <ThemeSwitch />
              <LanguageSwitcher />
            </div>
            <Button
              className='rounded-full'
              render={<Link to={destination} onClick={() => setOpen(false)} />}
            >
              {cta}
              <ArrowUpRight size={16} />
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
      <AuthPromptDialog prompt={authPrompt} />
    </>
  )
}
