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
import { ChevronRight } from 'lucide-react'
import {
  AnimatePresence,
  motion,
  useMotionValue,
  useReducedMotion,
  useSpring,
  useTransform,
  type MotionValue,
} from 'motion/react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import {
  buildDockItems,
  type DockLink,
  type NavigationDockTarget,
} from '../lib/navigation-dock-items'
import type { TopNavLink } from '../types'

import './navigation-dock.css'

type DockSpring = {
  damping: number
  mass: number
  stiffness: number
}

type NavigationDockProps = {
  activeTarget?: NavigationDockTarget
  className?: string
  links?: TopNavLink[]
  onLinkClick?: (
    event: React.MouseEvent<HTMLAnchorElement>,
    link: TopNavLink
  ) => void
  spring?: DockSpring
  magnification?: number
  distance?: number
  panelHeight?: number
  dockHeight?: number
  baseItemSize?: number
}

type DockItemProps = {
  active: boolean
  item: DockLink
  mouseX: MotionValue<number>
  spring: DockSpring
  distance: number
  magnification: number
  baseItemSize: number
  hidden: boolean
  isMobile: boolean
  onLinkClick?: NavigationDockProps['onLinkClick']
}

function DockItem(props: DockItemProps) {
  const ref = useRef<HTMLDivElement>(null)
  const isHovered = useMotionValue(0)
  const mouseDistance = useTransform(props.mouseX, (value) => {
    const rect = ref.current?.getBoundingClientRect() ?? {
      x: 0,
      width: props.baseItemSize,
    }
    return value - rect.x - rect.width / 2
  })
  const targetSize = useTransform(
    mouseDistance,
    [-props.distance, 0, props.distance],
    [props.baseItemSize, props.magnification, props.baseItemSize]
  )
  const size = useSpring(targetSize, props.spring)

  return (
    <motion.div
      ref={ref}
      className='nav-dock-item'
      style={{ height: size, width: size }}
      whileTap={
        props.isMobile && props.magnification > props.baseItemSize
          ? {
              scale: props.magnification / props.baseItemSize,
              zIndex: 10,
            }
          : undefined
      }
      transition={{
        type: 'spring',
        ...props.spring,
      }}
      onHoverStart={() => isHovered.set(1)}
      onHoverEnd={() => isHovered.set(0)}
      onFocus={() => isHovered.set(1)}
      onBlur={() => isHovered.set(0)}
    >
      <Link
        to={props.item.href}
        disabled={props.item.disabled}
        aria-current={props.active ? 'page' : undefined}
        aria-label={props.item.title}
        tabIndex={props.hidden ? -1 : undefined}
        className={cn(
          'nav-dock-link',
          props.active && 'nav-dock-link-active',
          props.item.disabled && 'pointer-events-none opacity-45'
        )}
        onClick={(event) => props.onLinkClick?.(event, props.item)}
      >
        <span className='nav-dock-icon' aria-hidden='true'>
          {props.item.icon}
        </span>
        <DockLabel isHovered={isHovered}>{props.item.title}</DockLabel>
      </Link>
    </motion.div>
  )
}

function DockLabel(props: {
  children: React.ReactNode
  isHovered: MotionValue<number>
}) {
  const [isVisible, setIsVisible] = useState(false)

  useEffect(
    () =>
      props.isHovered.on('change', (latest) => {
        setIsVisible(latest === 1)
      }),
    [props.isHovered]
  )

  return (
    <AnimatePresence>
      {isVisible && (
        <motion.span
          initial={{ opacity: 0, y: 0 }}
          animate={{ opacity: 1, y: -8 }}
          exit={{ opacity: 0, y: 0 }}
          transition={{ duration: 0.16 }}
          className='nav-dock-label'
          role='tooltip'
          style={{ x: '-50%' }}
        >
          {props.children}
        </motion.span>
      )}
    </AnimatePresence>
  )
}

export function NavigationDock({
  activeTarget,
  className,
  links = [],
  onLinkClick,
  spring = { mass: 0.1, stiffness: 150, damping: 12 },
  magnification = 76,
  distance = 210,
  panelHeight = 58,
  dockHeight = 116,
  baseItemSize = 46,
}: NavigationDockProps) {
  const { t } = useTranslation()
  const shouldReduceMotion = useReducedMotion()
  const mouseX = useMotionValue(Infinity)
  const isHovered = useMotionValue(0)
  const [isMobile, setIsMobile] = useState(false)
  const [isCollapsed, setIsCollapsed] = useState(false)
  const collapseTimerRef = useRef<number | null>(null)

  useEffect(() => {
    const mediaQuery = window.matchMedia('(max-width: 767px)')
    const updateMobileState = () => setIsMobile(mediaQuery.matches)

    updateMobileState()
    mediaQuery.addEventListener('change', updateMobileState)

    return () => mediaQuery.removeEventListener('change', updateMobileState)
  }, [])

  const canAutoCollapse = isMobile && activeTarget === 'console'
  const clearCollapseTimer = useCallback(() => {
    if (collapseTimerRef.current === null) return

    window.clearTimeout(collapseTimerRef.current)
    collapseTimerRef.current = null
  }, [])
  const scheduleCollapse = useCallback(() => {
    clearCollapseTimer()
    if (!canAutoCollapse) return

    collapseTimerRef.current = window.setTimeout(() => {
      setIsCollapsed(true)
      collapseTimerRef.current = null
    }, 3000)
  }, [canAutoCollapse, clearCollapseTimer])

  useEffect(() => {
    if (!canAutoCollapse) {
      setIsCollapsed(false)
      clearCollapseTimer()
      return
    }

    setIsCollapsed(false)
    scheduleCollapse()
    return clearCollapseTimer
  }, [canAutoCollapse, clearCollapseTimer, scheduleCollapse])

  const handleDockInteraction = useCallback(() => {
    if (!canAutoCollapse) return

    setIsCollapsed(false)
    scheduleCollapse()
  }, [canAutoCollapse, scheduleCollapse])

  const items = useMemo(() => buildDockItems(links, t), [links, t])

  const resolvedMagnification = shouldReduceMotion
    ? baseItemSize
    : magnification
  const maxHeight = Math.max(
    dockHeight,
    resolvedMagnification + resolvedMagnification / 2 + 4
  )
  const heightRow = useTransform(
    isHovered,
    [0, 1],
    [panelHeight, shouldReduceMotion ? panelHeight : maxHeight]
  )
  const height = useSpring(heightRow, spring)

  return (
    <motion.div
      style={{ height, scrollbarWidth: 'none' }}
      className={cn(
        'nav-dock-outer',
        isMobile && 'nav-dock-mobile',
        canAutoCollapse && 'nav-dock-auto-collapsible',
        isCollapsed && 'nav-dock-collapsed',
        className
      )}
    >
      <motion.nav
        onMouseMove={(event) => {
          if (shouldReduceMotion || isMobile) return
          isHovered.set(1)
          mouseX.set(event.clientX)
        }}
        onMouseLeave={() => {
          isHovered.set(0)
          mouseX.set(Infinity)
        }}
        onPointerDown={handleDockInteraction}
        className='nav-dock-panel'
        style={{ height: panelHeight }}
        aria-label={t('Primary navigation')}
        aria-hidden={isCollapsed || undefined}
      >
        {items.map((item) => (
          <DockItem
            key={item.target}
            item={item}
            active={activeTarget === item.target}
            mouseX={mouseX}
            spring={spring}
            distance={distance}
            magnification={resolvedMagnification}
            baseItemSize={baseItemSize}
            hidden={isCollapsed}
            isMobile={isMobile}
            onLinkClick={onLinkClick}
          />
        ))}
      </motion.nav>
      {canAutoCollapse && isCollapsed && (
        <button
          type='button'
          className='nav-dock-handle'
          aria-label={t('Expand navigation')}
          aria-expanded='false'
          onClick={() => {
            setIsCollapsed(false)
            scheduleCollapse()
          }}
        >
          <ChevronRight aria-hidden='true' />
        </button>
      )}
    </motion.div>
  )
}
