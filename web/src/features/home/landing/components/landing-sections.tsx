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
import { type LucideIcon, Activity, Gauge, Route } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { useTopNavLinks } from '@/hooks/use-top-nav-links'

type LandingCard = {
  icon: LucideIcon
  title: string
  body: string
}

const CARDS: LandingCard[] = [
  {
    icon: Route,
    title: 'Routing you can reason about',
    body: 'Per-model weights, priorities and sticky sessions, so failover is a decision you made instead of a mystery you debug.',
  },
  {
    icon: Gauge,
    title: 'Spend limits that bite early',
    body: 'Set quotas per token, per model or per group. The gateway refuses the request before your upstream invoice does.',
  },
  {
    icon: Activity,
    title: 'Health you can act on',
    body: 'Latency, error rate and success ratio per channel, with a shadow-mode audit that logs before it ever disables.',
  },
]

export function LandingSections({
  isAuthenticated,
}: {
  isAuthenticated: boolean
}) {
  const { t } = useTranslation()
  // Same source as the header: a module the operator disabled in 导航配置 is
  // absent from the list, so the CTA is hidden rather than left to bounce off
  // the `/about` guard back to the landing page.
  const showAbout = useTopNavLinks().some((link) => link.href === '/about')

  return (
    <>
      <section className='landing-section'>
        <h2>
          {t('A gateway you can read at a glance.')}{' '}
          <span>
            {t(
              'Routing, quotas and channel health on one screen instead of four.'
            )}
          </span>
        </h2>

        <div className='landing-grid'>
          <article className='landing-card landing-card-featured'>
            <div>
              <span className='landing-card-icon' aria-hidden='true'>
                <Route size={16} />
              </span>
              <h3>{t('Every channel lands in one ledger.')}</h3>
              <p>
                {t(
                  'Add as many upstreams as you like and they all speak the same dialect: the same request in, the same response out, the same log line afterwards.'
                )}
              </p>
            </div>
            <div className='landing-artwork' aria-hidden='true'>
              <Route
                className='landing-artwork-glyph'
                size={72}
                strokeWidth={1}
              />
            </div>
          </article>

          {CARDS.map((card) => (
            <article key={card.title} className='landing-card'>
              <span className='landing-card-icon' aria-hidden='true'>
                <card.icon size={16} />
              </span>
              <h3>{t(card.title)}</h3>
              <p>{t(card.body)}</p>
            </article>
          ))}
        </div>
      </section>

      <section className='landing-cta'>
        <h2>{t('Ready when you are.')}</h2>
        <p>
          {t(
            'Run it on your own domain. Nothing leaves your server except the calls you deliberately forward.'
          )}
        </p>
        <div className='landing-cta-actions'>
          <Link
            to={isAuthenticated ? '/dashboard' : '/sign-in'}
            className='landing-button landing-button-primary'
          >
            {isAuthenticated ? t('Go to Dashboard') : t('Get started')}
          </Link>
          {showAbout ? (
            <Link to='/about' className='landing-button'>
              {t('About')}
            </Link>
          ) : null}
        </div>
      </section>
    </>
  )
}
