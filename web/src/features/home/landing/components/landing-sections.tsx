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
import { useTranslation } from 'react-i18next'

type LandingCard = {
  title: string
  body: string
}

const CARDS: LandingCard[] = [
  {
    title: 'Routing you can reason about',
    body: 'Per-model weights, priorities and sticky sessions, so failover is a decision you made instead of a mystery you debug.',
  },
  {
    title: 'Spend limits that bite early',
    body: 'Set quotas per token, per model or per group. The gateway refuses the request before your upstream invoice does.',
  },
  {
    title: 'Health you can act on',
    body: 'Latency, error rate and success ratio per channel, with a shadow-mode audit that logs before it ever disables.',
  },
]

export function LandingSections() {
  const { t } = useTranslation()

  return (
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
        {/* The lead card carries the section's main claim, so it spans the
            full grid and gets the large type. It used to pair this copy with a
            gradient artwork block; the artwork was decoration that added a
            second accent to the page for no information. */}
        <article className='landing-card landing-card-lead'>
          <h3>{t('Every channel lands in one ledger.')}</h3>
          <p>
            {t(
              'Add as many upstreams as you like and they all speak the same dialect: the same request in, the same response out, the same log line afterwards.'
            )}
          </p>
        </article>

        {CARDS.map((card) => (
          <article key={card.title} className='landing-card'>
            <h3>{t(card.title)}</h3>
            <p>{t(card.body)}</p>
          </article>
        ))}
      </div>
    </section>
  )
}
