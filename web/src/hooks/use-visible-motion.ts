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
import { useCallback, useEffect, useRef, useState } from 'react'

export type VisibleMotionRef<T extends HTMLElement> = React.RefCallback<T> & {
  readonly current: T | null
}

/**
 * Motion gate for an animated surface.
 *
 * `playing` is false in three cases, and all three matter:
 *   - the element is scrolled out of view (nothing to watch),
 *   - the tab is in the background (a requestAnimationFrame loop keeps
 *     running in a hidden tab on most browsers; it just is not painted),
 *   - the user asked for reduced motion.
 *
 * The last one is reported separately as `reducedMotion` because a caller
 * usually still needs to show the *end* state of the animation, not nothing.
 */
export function useVisibleMotion<T extends HTMLElement>(paused = false) {
  const [element, setElement] = useState<T | null>(null)
  const elementRef = useRef<T | null>(null)
  const [visible, setVisible] = useState(false)
  const [hidden, setHidden] = useState(() =>
    typeof document !== 'undefined' ? document.hidden : false
  )
  const [reducedMotion, setReducedMotion] = useState(() =>
    typeof window !== 'undefined'
      ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
      : false
  )

  const ref = useCallback((node: T | null) => {
    elementRef.current = node
    setElement((prev) => (prev === node ? prev : node))
  }, []) as unknown as VisibleMotionRef<T>

  Object.defineProperty(ref, 'current', {
    get: () => elementRef.current,
    configurable: true,
  })

  useEffect(() => {
    if (!element) {
      setVisible(false)
      return
    }

    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const onMotionChange = () => setReducedMotion(media.matches)
    const onVisibilityChange = () => setHidden(document.hidden)
    const observer = new IntersectionObserver(
      ([entry]) => setVisible(entry.isIntersecting),
      { threshold: 0 }
    )

    observer.observe(element)
    media.addEventListener('change', onMotionChange)
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      observer.disconnect()
      media.removeEventListener('change', onMotionChange)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [element])

  return {
    ref,
    reducedMotion,
    playing: Boolean(
      element && visible && !hidden && !reducedMotion && !paused
    ),
  }
}

/**
 * Shared clock for a scripted animation.
 *
 * Two details are load-bearing. The clock is capped at 30fps, because a
 * typewriter either advances a character or it does not and 144Hz buys nothing.
 * And a single frame's delta is clamped to 100ms, so a backgrounded tab or a
 * long task does not fast-forward the script past the end on the next frame —
 * elapsed time survives the pause, it is never bulk-replayed.
 */
export function useVisibleAnimationFrame(
  playing: boolean,
  draw: (elapsed: number) => void
) {
  const callback = useRef(draw)
  const elapsed = useRef(0)

  useEffect(() => {
    callback.current = draw
  }, [draw])

  useEffect(() => {
    if (!playing) return

    let frame = 0
    let previous = performance.now()
    let rendered = -Infinity

    const tick = (now: number) => {
      elapsed.current += Math.min(now - previous, 100)
      previous = now
      if (now - rendered >= 1000 / 30) {
        callback.current(elapsed.current)
        rendered = now
      }
      frame = requestAnimationFrame(tick)
    }

    frame = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(frame)
  }, [playing])
}
