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
import {
  SideNav,
  SideNavCollapseButton,
  SideNavItem,
  SideNavSection,
} from '@astryxdesign/core/SideNav'
import { Link, useLocation } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { useState, type CSSProperties, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import type {
  NavCollapsible,
  NavItem,
  NavLink,
} from '@/components/layout/types'
import { useSidebarView } from '@/hooks/use-sidebar-view'
import { useSystemConfig } from '@/hooks/use-system-config'
import { DEFAULT_LOGO } from '@/lib/constants'

import { checkIsActive } from '../lib/url-utils'

function getNavigationHref(url: NavLink['url']): string | undefined {
  if (typeof url === 'string') return url
  if (!url || typeof url !== 'object' || Array.isArray(url)) return undefined

  const value = url as Record<string, unknown>
  const pathname = typeof value.pathname === 'string' ? value.pathname : ''
  const search = typeof value.search === 'string' ? value.search : ''
  return pathname ? `${pathname}${search}` : undefined
}

function NavigationBadge(props: { children: string }) {
  return <span className='snowapi-astryx-nav-badge'>{props.children}</span>
}

function NavigationItem(props: { item: NavItem; currentHref: string }) {
  if (props.item.type === 'chat-presets') {
    return null
  }

  const isSelected = checkIsActive(props.currentHref, props.item)
  const icon = props.item.icon
  const endContent = props.item.badge ? (
    <NavigationBadge>{props.item.badge}</NavigationBadge>
  ) : undefined

  if (!props.item.items) {
    const item = props.item as NavLink
    return (
      <SideNavItem
        label={item.title}
        icon={icon}
        href={getNavigationHref(item.url)}
        isSelected={isSelected}
        endContent={endContent}
      />
    )
  }

  const item = props.item as NavCollapsible
  const visibleItems = item.items
  if (!visibleItems || visibleItems.length === 0) return null
  const hasSelectedChild = visibleItems.some((child) =>
    checkIsActive(props.currentHref, child)
  )

  return (
    <SideNavItem
      key={`${item.title}-${isSelected}`}
      label={item.title}
      icon={icon}
      isSelected={isSelected && !hasSelectedChild}
      endContent={endContent}
      collapsible={{ defaultIsCollapsed: !isSelected }}
    >
      {visibleItems.map((subItem) => (
        <SideNavItem
          key={`${subItem.title}-${String(subItem.url)}`}
          label={subItem.title}
          icon={subItem.icon}
          href={getNavigationHref(subItem.url)}
          isSelected={checkIsActive(props.currentHref, subItem)}
          endContent={
            subItem.badge ? (
              <NavigationBadge>{subItem.badge}</NavigationBadge>
            ) : undefined
          }
        />
      ))}
    </SideNavItem>
  )
}

export function AstryxNavigation(props: { logo: string; footer?: ReactNode }) {
  const { t } = useTranslation()
  const { systemName } = useSystemConfig()
  const currentHref = useLocation({ select: (location) => location.href })
  const { key, view, navGroups } = useSidebarView()
  const visibleGroups = navGroups
    .map((group) => ({
      ...group,
      items: group.items.filter(
        (item) =>
          item.type !== 'chat-presets' && (!item.items || item.items.length > 0)
      ),
    }))
    .filter((group) => group.items.length > 0)
  const [isCollapsed, setIsCollapsed] = useState(false)
  const sideNavStyle = {
    '--snowapi-side-nav-width': isCollapsed ? '4.25rem' : '16rem',
  } as CSSProperties

  return (
    <SideNav
      className='snowapi-astryx-side-nav'
      style={sideNavStyle}
      data-snowapi-collapsed={isCollapsed || undefined}
      collapsible={{
        buttonLabel: t('Toggle sidebar'),
        isCollapsed,
        onCollapsedChange: setIsCollapsed,
        hasButton: false,
      }}
      header={
        <div className='snowapi-astryx-brand-row'>
          <Link
            to='/dashboard'
            className='snowapi-astryx-logo-link'
            aria-label={systemName}
          >
            <img
              src={props.logo || DEFAULT_LOGO}
              alt=''
              width={24}
              height={24}
              onError={(event) => {
                if (event.currentTarget.src.endsWith(DEFAULT_LOGO)) return
                event.currentTarget.src = DEFAULT_LOGO
              }}
            />
            <span className='snowapi-brand-name'>{systemName}</span>
          </Link>
          <SideNavCollapseButton className='snowapi-astryx-collapse-button' />
        </div>
      }
      topContent={
        view ? (
          <SideNavItem
            icon={ArrowLeft}
            label={t(view.parent.label)}
            href={getNavigationHref(view.parent.to)}
          />
        ) : undefined
      }
      footer={props.footer}
    >
      <div key={key} className='snowapi-astryx-side-nav-content'>
        {visibleGroups.map((group) => (
          <SideNavSection
            key={group.id || group.title}
            title={group.title}
            isHeaderHidden={isCollapsed || !group.title}
          >
            {group.items.map((item) => (
              <NavigationItem
                key={`${item.title}-${item.type || 'item'}`}
                item={item}
                currentHref={currentHref}
              />
            ))}
          </SideNavSection>
        ))}
      </div>
    </SideNav>
  )
}
