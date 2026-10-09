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
import { ArrowUpRight, Pause, Play } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ShaderArtwork } from '@/components/visuals/shader-artwork'
import { useSystemConfig } from '@/hooks/use-system-config'

type AuthLayoutProps = { children: React.ReactNode }

export function AuthLayout(props: AuthLayoutProps) {
  const { t } = useTranslation()
  const { systemName, logo, loading } = useSystemConfig()
  const [paused, setPaused] = useState(false)

  return (
    <div className='auth-shell'>
      <header className='auth-header'>
        <Link to='/' className='auth-brand'>
          {loading ? (
            <Skeleton className='size-8 rounded-lg' />
          ) : (
            <img src={logo} alt='' width={32} height={32} />
          )}
          {loading ? (
            <Skeleton className='h-6 w-24' />
          ) : (
            <span>{systemName}</span>
          )}
        </Link>
        <div className='auth-header-actions'>
          <LanguageSwitcher />
          <Link to='/'>
            {t('Back to home')}
            <ArrowUpRight size={14} />
          </Link>
        </div>
      </header>
      <main className='auth-stage'>
        <div className='auth-art-panel'>
          <div className='auth-artwork'>
            <ShaderArtwork variant='metal' paused={paused} />
          </div>
          <div className='auth-art-copy'>
            <p>{t('Your workspace, connected.')}</p>
            <span>{t('One key. A world of possibilities.')}</span>
          </div>
          <Button
            variant='ghost'
            size='icon'
            className='auth-motion-toggle'
            aria-label={paused ? t('Play animation') : t('Pause animation')}
            aria-pressed={paused}
            onClick={() => setPaused((value) => !value)}
          >
            {paused ? <Play /> : <Pause />}
          </Button>
        </div>
        <div className='auth-form-panel'>
          <div className='auth-form-content'>{props.children}</div>
        </div>
      </main>
      <footer className='auth-bottom'>
        <span>{systemName}</span>
        <span>{t('An open gateway to AI')}</span>
      </footer>
    </div>
  )
}
