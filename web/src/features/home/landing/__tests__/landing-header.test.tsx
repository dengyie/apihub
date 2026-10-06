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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import type React from 'react'
import { beforeAll, beforeEach, describe, expect, test, vi } from 'vitest'

import type { TopNavLink } from '@/components/layout/types'

import { LandingHeader } from '../components/landing-header'

const links: TopNavLink[] = []
const navigate = vi.fn()

vi.mock('@/hooks/use-top-nav-links', () => ({
  useTopNavLinks: () => links,
}))

vi.mock('@/components/theme-switch', () => ({ ThemeSwitch: () => null }))
vi.mock('@/components/language-switcher', () => ({
  LanguageSwitcher: () => null,
}))

vi.mock('@/components/dialog', () => ({
  Dialog: ({
    open,
    title,
    description,
    children,
  }: {
    open?: boolean
    title?: React.ReactNode
    description?: React.ReactNode
    children?: React.ReactNode
  }) =>
    open ? (
      <div role='dialog' aria-label={typeof title === 'string' ? title : ''}>
        <p>{title}</p>
        <p>{description}</p>
        {children}
      </div>
    ) : null,
}))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  // Standing up a route tree just to read an href is not worth it; the anchor
  // keeps the href and tabindex assertions meaningful.
  Link: ({
    to,
    children,
    ...rest
  }: {
    to: string
    children?: React.ReactNode
  }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

function renderHeader(isAuthenticated = false) {
  return render(
    <LandingHeader brand='New API' isAuthenticated={isAuthenticated} />
  )
}

describe('landing header nav links', () => {
  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', { Console: '控制台' })
  })

  beforeEach(() => {
    links.length = 0
    navigate.mockClear()
  })

  test('opens an external link in a new tab with rel=noopener', () => {
    links.push({ title: 'Docs', href: 'https://example.com', external: true })
    renderHeader()

    const link = screen.getByRole('link', { name: 'Docs' })
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  test('renders an internal link in place', () => {
    links.push({ title: 'About', href: '/about' })
    renderHeader()

    const link = screen.getByRole('link', { name: 'About' })
    expect(link).toHaveAttribute('href', '/about')
    expect(link).not.toHaveAttribute('target')
  })

  test('marks a disabled link and takes it out of the tab order', () => {
    links.push({ title: 'Rankings', href: '/rankings', disabled: true })
    renderHeader()

    const link = screen.getByRole('link', { name: 'Rankings' })
    expect(link).toHaveAttribute('aria-disabled', 'true')
    expect(link).toHaveAttribute('tabindex', '-1')
  })

  test('does not navigate for a disabled link', async () => {
    links.push({ title: 'Rankings', href: '/rankings', disabled: true })
    renderHeader()

    await userEvent.click(screen.getByRole('link', { name: 'Rankings' }))

    expect(navigate).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  // The behaviour public-header has always had and the landing header skipped:
  // a link gated by 运营设置 → 导航配置 → requireAuth must not go through.
  test('blocks navigation for a sign-in-gated link and prompts instead', async () => {
    links.push({ title: 'Model Square', href: '/pricing', requiresAuth: true })
    renderHeader()

    await userEvent.click(screen.getByRole('link', { name: 'Model Square' }))

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('Model Square')).toBeInTheDocument()
    expect(navigate).not.toHaveBeenCalled()
  })

  test('does not prompt for a link that needs no sign-in', async () => {
    links.push({ title: 'About', href: '/about' })
    renderHeader()

    await userEvent.click(screen.getByRole('link', { name: 'About' }))

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  // `useTopNavLinks` omits modules the operator disabled, so an empty list
  // means "nothing configured" — the header must not invent replacements.
  test('renders only the brand and CTA when the operator configured no links', () => {
    renderHeader()

    expect(screen.getAllByRole('link')).toHaveLength(2)
  })

  test.each([
    { isAuthenticated: false, href: '/sign-in', label: 'Sign in' },
    { isAuthenticated: true, href: '/dashboard', label: 'Go to Dashboard' },
  ])(
    'points the CTA at $href when isAuthenticated=$isAuthenticated',
    ({ isAuthenticated, href, label }) => {
      renderHeader(isAuthenticated)

      expect(
        screen.getByRole('link', { name: new RegExp(label) })
      ).toHaveAttribute('href', href)
    }
  )

  test('toggles the mobile menu', async () => {
    renderHeader()

    const toggle = screen.getByRole('button', { name: /Menu/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')

    await userEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')

    await userEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })

  test('closes the mobile menu on Escape', async () => {
    renderHeader()

    const toggle = screen.getByRole('button', { name: /Menu/ })
    await userEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')

    await userEvent.type(toggle, '{Escape}')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })

  test('keeps the menu open while a prompt is showing', async () => {
    links.push({ title: 'Rankings', href: '/rankings', requiresAuth: true })
    renderHeader()

    await userEvent.click(screen.getByRole('button', { name: /Menu/ }))
    await userEvent.click(screen.getByRole('link', { name: 'Rankings' }))

    expect(screen.getByRole('button', { name: /Menu/ })).toHaveAttribute(
      'aria-expanded',
      'true'
    )
  })

  // `useTopNavLinks` localizes before this component sees the link, so calling
  // t() again here would translate an already-translated string. The bundle
  // below maps 'Console' to something visibly different, which is what makes a
  // double translation detectable.
  test('renders the title as given instead of translating it again', () => {
    links.push({ title: 'Console', href: '/dashboard' })

    renderHeader()

    expect(screen.getByRole('link', { name: 'Console' })).toBeInTheDocument()
    expect(
      screen.queryByRole('link', { name: '控制台' })
    ).not.toBeInTheDocument()
  })
})
