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

import {
  useVisibleAnimationFrame,
  useVisibleMotion,
} from '@/hooks/use-visible-motion'

export function ShuffleText(props: { text: string; className?: string }) {
  const [finished, setFinished] = useState(false)
  const [display, setDisplay] = useState(props.text)
  const start = useRef<number | null>(null)
  const { ref, playing, reducedMotion } =
    useVisibleMotion<HTMLSpanElement>(finished)

  useEffect(() => {
    start.current = null
    setFinished(false)
    setDisplay(props.text)
  }, [props.text])

  useVisibleAnimationFrame(playing, (elapsed) => {
    start.current ??= elapsed
    const progress = Math.min((elapsed - start.current) / 850, 1)
    if (progress === 1) {
      setDisplay(props.text)
      setFinished(true)
      return
    }
    const characters = [...props.text]
    const glyphs = '01/<>_+'
    setDisplay(
      characters
        .map((char, i) =>
          i / characters.length <= progress || /\s/u.test(char)
            ? char
            : glyphs[(i + Math.floor(elapsed / 70)) % glyphs.length]
        )
        .join('')
    )
  })

  return (
    <span ref={ref} className={props.className}>
      <span className='sr-only'>{props.text}</span>
      <span aria-hidden='true'>
        {reducedMotion || finished ? props.text : display}
      </span>
    </span>
  )
}
