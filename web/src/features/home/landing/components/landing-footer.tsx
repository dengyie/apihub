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

import { LanguageSwitcher } from '@/components/language-switcher'
import { ThemeSwitch } from '@/components/theme-switch'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { cn } from '@/lib/utils'

import { LandingWordmark } from './landing-wordmark'

export function LandingFooter({ brand }: { brand: string }) {
  // Same source as the header: a module the operator disabled in 导航配置 is
  // absent from the list rather than replaced by a fabricated entry.
  const links = useTopNavLinks()

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
              className={cn(link.disabled && 'opacity-50')}
              aria-disabled={link.disabled}
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
            >
              {link.title}
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
