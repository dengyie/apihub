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
import { render } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { LandingHero } from '../components/landing-hero'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (value: string) => value }),
}))

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...props }: { children?: ReactNode }) => (
    <a {...props}>{children}</a>
  ),
}))

vi.mock('@/components/layout/components/auth-prompt', () => ({
  AuthPromptDialog: () => null,
}))

vi.mock('@/components/ui/button', () => ({
  Button: ({ children }: { children?: ReactNode }) => (
    <button type='button'>{children}</button>
  ),
}))

vi.mock('@/hooks/use-auth-prompt', () => ({
  useAuthPrompt: () => ({ interceptLinkClick: vi.fn() }),
}))

vi.mock('@/hooks/use-top-nav-links', () => ({
  useTopNavLinks: () => [{ href: '/pricing', disabled: false }],
}))

vi.mock('../components/hero-gateway', () => ({
  HeroGateway: () => <div />,
}))

vi.mock('../components/landing-wordmark', () => ({
  LandingWordmark: () => <div />,
}))

vi.mock('../components/shuffle-text', () => ({
  ShuffleText: ({ text }: { text: string }) => <span>{text}</span>,
}))

describe('landing hero providers', () => {
  test('shows the supported providers in the public order', () => {
    const { container } = render(
      <LandingHero brand='MangoApi' logo='/logo.svg' isAuthenticated={false} />
    )
    const providerNames = [
      ...container.querySelectorAll('.landing-providers > div > span'),
    ].map((element) => element.textContent)

    expect(providerNames).toEqual([
      'OpenAI',
      'Anthropic',
      'Gemini',
      'Grok',
      'Deepseek',
    ])
    expect(container.querySelector('.provider-qwen')).not.toBeInTheDocument()
    expect(container).not.toHaveTextContent('Qwen')
  })
})
