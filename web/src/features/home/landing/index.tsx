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

import { useSystemConfig } from '@/hooks/use-system-config'
import { useAuthStore } from '@/stores/auth-store'

import { LandingFooter } from './components/landing-footer'
import { LandingHeader } from './components/landing-header'
import { LandingHero } from './components/landing-hero'
import { LandingSections } from './components/landing-sections'

export function LandingPage() {
  const { t } = useTranslation()
  const { systemName, logo } = useSystemConfig()
  const { auth } = useAuthStore()
  const isAuthenticated = !!auth.user
  const brand = systemName

  return (
    <div className='landing-root'>
      <a className='landing-skip' href='#landing-content'>
        {t('Skip to content')}
      </a>

      <div className='landing-shell'>
        <LandingHeader
          logo={logo}
          brand={brand}
          isAuthenticated={isAuthenticated}
        />

        <main id='landing-content' className='landing-main'>
          <LandingHero
            brand={brand}
            logo={logo}
            isAuthenticated={isAuthenticated}
          />
          <LandingSections />
        </main>

        <LandingFooter brand={brand} />
      </div>
    </div>
  )
}
