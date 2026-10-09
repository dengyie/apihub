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
import gsap from 'gsap'
import { Children, useEffect, useRef, useState, type ReactNode } from 'react'

import {
  useVisibleAnimationFrame,
  useVisibleMotion,
} from '@/hooks/use-visible-motion'

type CardSwapProps = { children: ReactNode; paused?: boolean }

export function CardSwap(props: CardSwapProps) {
  const [hovered, setHovered] = useState(false)
  const { ref, playing, reducedMotion } = useVisibleMotion<HTMLDivElement>(
    props.paused || hovered
  )
  const timeline = useRef<gsap.core.Timeline | null>(null)
  const count = Children.count(props.children)

  useEffect(() => {
    const cards =
      ref.current?.querySelectorAll<HTMLElement>('.gateway-card-slot')
    if (!cards?.length || reducedMotion) return
    // A paused timeline is advanced by our shared 30fps clock, so GSAP never
    // keeps its own infinite ticker alive on a hidden tab.
    const animation = gsap.timeline({ paused: true, repeat: -1 })
    cards.forEach((card, index) => {
      gsap.set(card, {
        x: index * 20,
        y: index * -22,
        z: index * -90,
        rotation: index * 3,
        zIndex: count - index,
      })
    })
    for (let turn = 0; turn < count; turn++) {
      const at = turn * 4.5 + 3
      const front = cards[turn]
      animation.to(
        front,
        { y: 130, opacity: 0, rotation: -8, duration: 0.42, ease: 'power2.in' },
        at
      )
      for (let offset = 1; offset < count; offset++) {
        const card = cards[(turn + offset) % count]
        animation.set(card, { zIndex: count - offset + 1 }, at + 0.3)
        animation.to(
          card,
          {
            x: (offset - 1) * 20,
            y: (offset - 1) * -22,
            z: (offset - 1) * -90,
            rotation: (offset - 1) * 3,
            duration: 0.8,
            ease: 'power3.inOut',
          },
          at + 0.2
        )
      }
      animation.set(
        front,
        {
          x: (count - 1) * 20,
          y: (count - 1) * -22,
          z: (count - 1) * -90,
          rotation: (count - 1) * 3,
          zIndex: 1,
        },
        at + 0.45
      )
      animation.to(front, { opacity: 1, duration: 0.55 }, at + 0.5)
    }
    animation.to({}, { duration: 0.45 })
    timeline.current = animation
    gsap.ticker.sleep()
    return () => {
      animation.kill()
      timeline.current = null
      gsap.set(cards, { clearProps: 'all' })
      gsap.ticker.sleep()
    }
  }, [count, reducedMotion, ref])

  useVisibleAnimationFrame(playing, (elapsed) => {
    timeline.current?.totalTime(elapsed / 1000)
    // GSAP wakes its ticker when totalTime changes. This is the only GSAP
    // surface in the app; the visibility-aware clock owns its scheduling.
    gsap.ticker.sleep()
  })

  return (
    <div
      ref={ref}
      className='gateway-card-stack'
      aria-hidden='true'
      onPointerEnter={() => setHovered(true)}
      onPointerLeave={() => setHovered(false)}
    >
      {Children.map(props.children, (child, index) => (
        <div
          className='gateway-card-slot'
          style={{ '--card-index': index } as React.CSSProperties}
        >
          {child}
        </div>
      ))}
    </div>
  )
}
