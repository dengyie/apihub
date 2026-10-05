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
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import type { TopNavLink } from '@/components/layout/types'
import { ThemeSwitch } from '@/components/theme-switch'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'

import { LandingWordmark } from './landing-wordmark'

const FALLBACK_LINKS: TopNavLink[] = [
  { title: 'Pricing', href: '/pricing' },
  { title: 'Rankings', href: '/rankings' },
  { title: 'About', href: '/about' },
]

export function LandingFooter({ brand }: { brand: string }) {
  const { t } = useTranslation()
  const dynamicLinks = useTopNavLinks()
  const links = dynamicLinks.length > 0 ? dynamicLinks : FALLBACK_LINKS

  return (
    <footer className='landing-footer'>
      <div className='landing-footer-nav'>
        {links.map((link) =>
          link.external ? (
            <a
              key={`${link.title}:${link.href}`}
              href={link.href}
              target='_blank'
              rel='noopener noreferrer'
            >
              {t(link.title)}
            </a>
          ) : (
            <Link key={`${link.title}:${link.href}`} to={link.href}>
              {t(link.title)}
            </Link>
          )
        )}

        <div className='landing-preferences'>
          <ThemeSwitch />
          <LanguageSwitcher />
        </div>
      </div>

      <LandingWordmark brand={brand} />
    </footer>
  )
}
