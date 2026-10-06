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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import { AuthPromptDialog } from '@/components/layout/components/auth-prompt'
import type { TopNavLink } from '@/components/layout/types'
import { ThemeSwitch } from '@/components/theme-switch'
import { useAuthPrompt } from '@/hooks/use-auth-prompt'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { cn } from '@/lib/utils'

export function LandingHeader({
  brand,
  isAuthenticated,
}: {
  brand: string
  isAuthenticated: boolean
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  // `useTopNavLinks` already honours 运营设置 → 导航配置: a module the
  // operator turned off is simply absent from the list, so there is nothing
  // to fall back to. Fabricating links here would render entries the
  // operator disabled.
  const links = useTopNavLinks()
  const authPrompt = useAuthPrompt()
  const close = () => setOpen(false)

  const handleLinkClick = (
    event: React.MouseEvent<HTMLAnchorElement>,
    link: TopNavLink
  ) => {
    // A disabled or sign-in-gated link must not dismiss the panel: the user
    // has to stay on the page to deal with the prompt.
    if (authPrompt.interceptLinkClick(event, link)) return
    close()
  }

  return (
    <>
      <header
        className='landing-header'
        onKeyDown={(event) => {
          if (event.key === 'Escape') close()
        }}
      >
        <Link to='/' className='landing-brand'>
          <span>{brand}</span>
        </Link>

        <button
          type='button'
          className='landing-menu-toggle'
          aria-expanded={open}
          aria-controls='landing-nav'
          onClick={() => setOpen((value) => !value)}
        >
          {t('Menu')}
          {open ? <X size={16} /> : <Menu size={16} />}
        </button>

        <nav
          id='landing-nav'
          className='landing-nav'
          data-open={open}
          aria-label={t('Navigation')}
        >
          <div className='landing-nav-links'>
            {links.map((link) =>
              link.external ? (
                <a
                  key={`${link.title}:${link.href}`}
                  href={link.href}
                  target='_blank'
                  rel='noopener noreferrer'
                  className={cn(link.disabled && 'opacity-50')}
                  aria-disabled={link.disabled}
                  tabIndex={link.disabled ? -1 : undefined}
                  onClick={(event) => handleLinkClick(event, link)}
                >
                  {link.title}
                </a>
              ) : (
                <Link
                  key={`${link.title}:${link.href}`}
                  to={link.href}
                  disabled={link.disabled}
                  className={cn(link.disabled && 'opacity-50')}
                  aria-disabled={link.disabled}
                  tabIndex={link.disabled ? -1 : undefined}
                  onClick={(event) => handleLinkClick(event, link)}
                >
                  {link.title}
                </Link>
              )
            )}
          </div>

          <div className='landing-preferences'>
            <ThemeSwitch />
            <LanguageSwitcher />
          </div>

          <Link
            to={isAuthenticated ? '/dashboard' : '/sign-in'}
            className='landing-button landing-button-primary'
            onClick={close}
          >
            {isAuthenticated ? t('Go to Dashboard') : t('Sign in')}
            <ArrowUpRight size={14} aria-hidden='true' />
          </Link>
        </nav>
      </header>

      <AuthPromptDialog prompt={authPrompt} />
    </>
  )
}
