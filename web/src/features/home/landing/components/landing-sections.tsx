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
import { Activity, ArrowUpRight, GitBranch, Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { HeroTerminalDemo } from './hero-terminal-demo'
import { LandingIntegration } from './landing-integration'

const features = [
  {
    icon: GitBranch,
    title: 'Routing you can reason about',
    body: 'Choose channels, set priorities and define fallback behavior. Your routing decisions stay in your control.',
  },
  {
    icon: Wallet,
    title: 'Know where your budget goes',
    body: 'Track usage, manage token quotas and review request costs in one place.',
  },
  {
    icon: Activity,
    title: 'Health you can act on',
    body: 'See model success rates, response times and hourly history based on real requests.',
  },
]

export function LandingSections() {
  const { t } = useTranslation()
  return (
    <>
      <LandingIntegration />
      <section
        className='landing-section landing-features'
        aria-labelledby='features-title'
      >
        <div className='landing-section-heading'>
          <p className='landing-eyebrow'>{t('Clarity at every layer')}</p>
          <h2 id='features-title'>
            {t('Less friction.')}
            <em>{t('More room to build.')}</em>
          </h2>
        </div>
        <div className='landing-feature-grid'>
          {features.map((feature, index) => (
            <article className='landing-feature' key={feature.title}>
              <div className='landing-feature-top'>
                <feature.icon size={22} strokeWidth={1.4} />
                <span>0{index + 1}</span>
              </div>
              <h3>{t(feature.title)}</h3>
              <p>{t(feature.body)}</p>
            </article>
          ))}
        </div>
        <details className='landing-routing-demo'>
          <summary>
            {t('Explore a routing example')}
            <ArrowUpRight size={16} />
          </summary>
          <p>
            {t('An illustrative routing session, not live service metrics.')}
          </p>
          <div className='landing-showcase'>
            <HeroTerminalDemo />
          </div>
        </details>
      </section>
    </>
  )
}
