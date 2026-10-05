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
import { useEffect, useRef, useState } from 'react'

/**
 * Latches true once the element has scrolled into view.
 *
 * The landing page's motion (wordmark stripe sweep) should start when the mark
 * is actually seen, not on mount, so a visitor who never scrolls past the fold
 * never pays for the animation.
 */
export function useLandingInView<T extends Element>(
  options?: IntersectionObserverInit
) {
  const ref = useRef<T>(null)
  const [inView, setInView] = useState(false)

  // Held in a ref so a caller passing an inline options literal does not
  // re-subscribe on every render.
  const optionsRef = useRef(options)
  optionsRef.current = options

  useEffect(() => {
    const node = ref.current
    if (!node || inView) return

    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setInView(true)
        observer.disconnect()
      }
    }, optionsRef.current)

    observer.observe(node)
    return () => observer.disconnect()
  }, [inView])

  return [ref, inView] as const
}
