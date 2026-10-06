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
import { ArrowUpRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { useTopNavLinks } from '@/hooks/use-top-nav-links'

import { HeroTerminalDemo } from './hero-terminal-demo'

export function LandingHero({
  brand,
  isAuthenticated,
}: {
  brand: string
  isAuthenticated: boolean
}) {
  const { t } = useTranslation()
  // Same source as the header: a module the operator disabled in 导航配置 is
  // absent from the list, so the CTA is hidden rather than left to bounce off
  // the `/pricing` guard back to the landing page.
  const showPricing = useTopNavLinks().some((link) => link.href === '/pricing')

  return (
    <section className='landing-hero'>
      <h1>
        <span className='landing-chip'>{brand}</span>
        {t('One gateway for every model you run.')}
      </h1>

      <p className='landing-hero-sub'>
        {t(
          'Point a single OpenAI-compatible key at your own deployment and route it to any upstream channel. Cost, latency and failures stay visible per model, not per guess.'
        )}
      </p>

      <div className='landing-hero-actions'>
        <Link
          to={isAuthenticated ? '/dashboard' : '/sign-in'}
          className='landing-button landing-button-primary'
        >
          {isAuthenticated ? t('Go to Dashboard') : t('Get started')}
          <ArrowUpRight size={14} aria-hidden='true' />
        </Link>
        {showPricing ? (
          <Link to='/pricing' className='landing-button'>
            {t('Model pricing')}
          </Link>
        ) : null}
      </div>

      <div className='landing-showcase'>
        <div className='landing-grain' aria-hidden='true' />
        <div className='landing-window'>
          <div className='landing-window-bar'>
            <span className='landing-window-dots' aria-hidden='true'>
              <i />
              <i />
              <i />
            </span>
            <span>{brand.toLowerCase().replaceAll(' ', '-')}</span>
            <span>/v1/chat/completions</span>
          </div>
          <div className='landing-window-body'>
            <HeroTerminalDemo />
          </div>
        </div>
      </div>
    </section>
  )
}
