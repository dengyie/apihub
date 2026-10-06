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
import { Boxes, House, LayoutDashboard } from 'lucide-react'

import type { TopNavLink } from '../types'

export type NavigationDockTarget = 'home' | 'console' | 'models'

export type DockLink = TopNavLink & {
  icon: React.ReactNode
  target: NavigationDockTarget
}

type DockTarget = {
  target: NavigationDockTarget
  title: string
  href: string
  icon: React.ReactNode
  /** Whether 运营设置 → 导航配置 controls this entry's visibility. */
  configurable: boolean
}

/**
 * Derives the dock entries from the operator's nav configuration.
 *
 * `useTopNavLinks` already honours 运营设置 → 导航配置: a module the operator
 * turned off is simply absent from `links`. Rendering it anyway would leave a
 * permanently visible entry that leads nowhere and silently defeats the
 * setting, so a configurable target with no matching link is dropped.
 *
 * `/models` is the one exception — no nav module produces it, so it stays a
 * fixed console shortcut.
 */
export function buildDockItems(
  links: TopNavLink[],
  translate: (key: string) => string
): DockLink[] {
  const targets: DockTarget[] = [
    {
      target: 'home',
      title: translate('Home'),
      href: '/',
      icon: <House />,
      configurable: true,
    },
    {
      target: 'console',
      title: translate('Console'),
      href: '/dashboard',
      icon: <LayoutDashboard />,
      configurable: true,
    },
    {
      target: 'models',
      title: translate('Model List'),
      href: '/models',
      icon: <Boxes />,
      configurable: false,
    },
  ]

  return targets.flatMap((target) => {
    const configured = links.find((link) => link.href === target.href)
    if (target.configurable && !configured) return []

    return [
      {
        ...configured,
        target: target.target,
        title: configured?.title || target.title,
        href: target.href,
        icon: target.icon,
      },
    ]
  })
}
