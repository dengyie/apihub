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
import type { TopNavLink } from '@/components/layout/types'
import { ThemeSwitch } from '@/components/theme-switch'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { cn } from '@/lib/utils'

/**
 * Shown when the operator has configured no top-nav links at all, so the
 * marketing header never collapses to an empty row.
 */
const FALLBACK_LINKS: TopNavLink[] = [
  { title: 'Pricing', href: '/pricing' },
  { title: 'Rankings', href: '/rankings' },
  { title: 'About', href: '/about' },
]

export function LandingHeader({
  brand,
  isAuthenticated,
}: {
  brand: string
  isAuthenticated: boolean
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const dynamicLinks = useTopNavLinks()
  const links = dynamicLinks.length > 0 ? dynamicLinks : FALLBACK_LINKS
  const close = () => setOpen(false)

  return (
    <header
      className='landing-header'
      onKeyDown={(event) => {
        if (event.key === 'Escape') close()
      }}
    >
      <Link to='/' className='landing-brand'>
        <span className='landing-brand-mark' aria-hidden='true'>
          {brand.slice(0, 1).toUpperCase()}
        </span>
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
        <div className='landing-nav-links' onClick={close}>
          {links.map((link) =>
            link.external ? (
              <a
                key={`${link.title}:${link.href}`}
                href={link.href}
                target='_blank'
                rel='noopener noreferrer'
                className={cn(link.disabled && 'opacity-50')}
                aria-disabled={link.disabled}
              >
                {t(link.title)}
              </a>
            ) : (
              <Link
                key={`${link.title}:${link.href}`}
                to={link.href}
                className={cn(link.disabled && 'opacity-50')}
                aria-disabled={link.disabled}
              >
                {t(link.title)}
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
  )
}
