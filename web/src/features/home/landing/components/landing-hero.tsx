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
import { ArrowRight, ArrowUpRight, Pause, Play } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AuthPromptDialog } from '@/components/layout/components/auth-prompt'
import { Button } from '@/components/ui/button'
import { useAuthPrompt } from '@/hooks/use-auth-prompt'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'

import { HeroGateway } from './hero-gateway'
import { ShuffleText } from './shuffle-text'

export function LandingHero(props: {
  brand: string
  isAuthenticated: boolean
}) {
  const { t } = useTranslation()
  const [paused, setPaused] = useState(false)
  const pricingLink = useTopNavLinks().find((link) => link.href === '/pricing')
  const authPrompt = useAuthPrompt()

  return (
    <>
      <section className='landing-hero'>
        <div className='landing-hero-copy'>
          <p className='landing-eyebrow'>
            <span className='landing-eyebrow-dot' />
            <ShuffleText text={t('An open gateway to AI')} />
          </p>
          <h1>
            {t('Your models.')}
            <em>{t('One gateway.')}</em>
          </h1>
          <p className='landing-hero-sub'>
            {t(
              'Connect OpenAI, Claude, Gemini and more through one compatible API. Manage keys, costs and routing from a single workspace.'
            )}
          </p>
          <div className='landing-hero-actions'>
            <Button
              size='lg'
              className='landing-primary'
              render={
                <Link to={props.isAuthenticated ? '/dashboard' : '/sign-in'} />
              }
            >
              {props.isAuthenticated ? t('Go to Dashboard') : t('Get started')}
              <ArrowUpRight size={17} />
            </Button>
            {pricingLink && (
              <Button
                size='lg'
                variant='ghost'
                className='landing-secondary'
                disabled={pricingLink.disabled}
                render={
                  <Link
                    to='/pricing'
                    onClick={(event) =>
                      authPrompt.interceptLinkClick(event, pricingLink)
                    }
                  />
                }
              >
                {t('Explore models')}
                <ArrowRight size={16} />
              </Button>
            )}
          </div>
          <div className='landing-hero-note'>
            <span>01 /</span>
            {t('Keep your SDK. Change your base URL.')}
          </div>
        </div>
        <div className='landing-hero-visual'>
          <HeroGateway brand={props.brand} paused={paused} />
          <Button
            variant='ghost'
            size='icon'
            className='landing-motion-toggle'
            aria-label={paused ? t('Play animation') : t('Pause animation')}
            aria-pressed={paused}
            onClick={() => setPaused((value) => !value)}
          >
            {paused ? <Play /> : <Pause />}
          </Button>
        </div>
      </section>
      <div
        className='landing-providers'
        aria-label={t('Supported API providers')}
      >
        <p>{t('Made for the models you already use')}</p>
        <div>
          <span className='provider-openai'>OpenAI</span>
          <span className='provider-claude'>Anthropic</span>
          <span className='provider-gemini'>Gemini</span>
          <span className='provider-deepseek'>deepseek</span>
          <span className='provider-qwen'>Qwen</span>
        </div>
      </div>
      <AuthPromptDialog prompt={authPrompt} />
    </>
  )
}
