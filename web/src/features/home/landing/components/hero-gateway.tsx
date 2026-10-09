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
import { ArrowUpRight, Check, GitBranch, KeyRound, Layers3 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ShaderArtwork } from '@/components/visuals/shader-artwork'

import { CardSwap } from './card-swap'

export function HeroGateway(props: { brand: string; paused: boolean }) {
  const { t } = useTranslation()
  return (
    <div className='hero-gateway'>
      <ShaderArtwork
        variant='threads'
        paused={props.paused}
        className='hero-threads'
      />
      <div className='gateway-orbit gateway-orbit-one' aria-hidden='true' />
      <div className='gateway-orbit gateway-orbit-two' aria-hidden='true' />
      <CardSwap paused={props.paused}>
        <article className='gateway-card'>
          <div className='gateway-card-header'>
            <span>
              <KeyRound size={16} />
              {t('One API key')}
            </span>
            <ArrowUpRight size={16} />
          </div>
          <div className='gateway-card-core'>
            <span className='gateway-symbol'>✳</span>
            <strong>{props.brand}</strong>
            <span>{t('Your models. One endpoint.')}</span>
          </div>
          <div className='gateway-key'>
            <span>sk-••••••••••••••••••••</span>
            <Check size={14} />
          </div>
          <div className='gateway-card-foot'>
            <span>{t('OpenAI-compatible')}</span>
            <code>/v1</code>
          </div>
        </article>
        <article className='gateway-card gateway-card-routing'>
          <div className='gateway-card-header'>
            <span>
              <GitBranch size={16} />
              {t('Flexible routing')}
            </span>
            <ArrowUpRight size={16} />
          </div>
          <div className='gateway-routing'>
            <span>OpenAI</span>
            <span>Anthropic</span>
            <span>Gemini</span>
            <span>DeepSeek</span>
          </div>
          <div className='gateway-card-foot'>
            <span>{t('Choose your upstream')}</span>
            <GitBranch size={16} />
          </div>
        </article>
        <article className='gateway-card gateway-card-usage'>
          <div className='gateway-card-header'>
            <span>
              <Layers3 size={16} />
              {t('Built for your workflow')}
            </span>
            <ArrowUpRight size={16} />
          </div>
          <div className='gateway-workflows'>
            <span>{t('Chat & reasoning')}</span>
            <span>{t('Images & audio')}</span>
            <span>{t('Tools & agents')}</span>
          </div>
          <div className='gateway-card-foot'>
            <span>{t('Keep the SDK you use')}</span>
            <Check size={16} />
          </div>
        </article>
      </CardSwap>
      <div className='gateway-caption'>
        <span className='gateway-caption-line' />
        {t('One interface. More possibilities.')}
      </div>
    </div>
  )
}
