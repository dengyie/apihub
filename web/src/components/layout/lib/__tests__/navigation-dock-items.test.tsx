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
import { describe, expect, test } from 'vitest'

import type { TopNavLink } from '../../types'
import { buildDockItems } from '../navigation-dock-items'

const identity = (key: string) => key

/** What `useTopNavLinks` returns under the default configuration. */
function defaultLinks(): TopNavLink[] {
  return [
    { title: 'Home', href: '/' },
    { title: 'Console', href: '/dashboard' },
    { title: 'Model Square', href: '/pricing' },
    { title: 'Docs', href: '/docs' },
    { title: 'About', href: '/about' },
  ]
}

describe('buildDockItems', () => {
  test('offers home, console and models under the default configuration', () => {
    expect(
      buildDockItems(defaultLinks(), identity).map((i) => i.target)
    ).toEqual(['home', 'console', 'models'])
  })

  // The regression this guards: the dock hardcoded its three entries and only
  // borrowed their titles, so switching a module off in 运营设置 → 导航配置 left
  // a permanently visible entry on every page of the app.
  test.each([
    { href: '/', disabled: 'home' },
    { href: '/dashboard', disabled: 'console' },
  ])(
    'drops the $disabled entry when $href is absent from the nav config',
    ({ href, disabled }) => {
      const links = defaultLinks().filter((link) => link.href !== href)
      const targets = buildDockItems(links, identity).map((i) => i.target)

      expect(targets).not.toContain(disabled)
      expect(targets).toHaveLength(2)
    }
  )

  test('keeps models even though no nav module produces it', () => {
    // `/models` lives under the authenticated layout and `useTopNavLinks`
    // never emits it, so it is not driven by 导航配置.
    expect(buildDockItems([], identity).map((i) => i.target)).toEqual([
      'models',
    ])
  })

  test('prefers the operator-configured title over the built-in one', () => {
    const items = buildDockItems(
      [{ title: 'Retour accueil', href: '/' }],
      identity
    )

    expect(items[0].title).toBe('Retour accueil')
  })

  test('falls back to the translated built-in title when the link carries none', () => {
    const items = buildDockItems([{ title: '', href: '/dashboard' }], identity)

    expect(items[0].title).toBe('Console')
  })

  test('matches targets by href, so a link elsewhere cannot stand in for one', () => {
    const items = buildDockItems(
      [{ title: 'Console', href: '/somewhere-else' }],
      identity
    )

    // `/somewhere-else` is not the console module, so console stays disabled.
    expect(items.map((i) => i.target)).toEqual(['models'])
  })

  test('carries requiresAuth through so the click interceptor can gate it', () => {
    const items = buildDockItems(
      [{ title: 'Console', href: '/dashboard', requiresAuth: true }],
      identity
    )
    const console_ = items.find((i) => i.target === 'console')

    expect(console_?.requiresAuth).toBe(true)
  })
})
