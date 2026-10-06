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

import {
  getTerminalFrame,
  TERMINAL_LINES,
  TERMINAL_PROMPT,
  type TerminalFrame,
} from '../lib/terminal-timeline'

/**
 * The hero's scripted terminal.
 *
 * It replaces the previous tab-switching request/response card. That card was
 * the only element on the page still painted in four accent colours, with a
 * backdrop blur and a drop shadow, and it fought everything around it. This is
 * one session, played once, on a three-colour palette.
 */
export function HeroTerminalDemo() {
  const { ref, playing, reducedMotion } = useVisibleMotion<HTMLDivElement>()
  const outputRef = useRef<HTMLDivElement>(null)
  const [frame, setFrame] = useState<TerminalFrame>(() =>
    getTerminalFrame(0, reducedMotion)
  )

  useVisibleAnimationFrame(playing, (elapsed) => {
    const next = getTerminalFrame(elapsed)
    // The clock runs at 30fps but the frame only changes a few times a second;
    // without this guard every intermediate frame re-renders the whole line
    // list for nothing.
    setFrame((previous) => (isSameFrame(previous, next) ? previous : next))
  })

  // Reduced motion pins the script to its last frame, so scroll into view.
  const current = reducedMotion ? getTerminalFrame(0, true) : frame

  useEffect(() => {
    const element = outputRef.current
    if (element) element.scrollTop = element.scrollHeight
  }, [current.lines, current.submitted])

  return (
    <div className='landing-terminal' ref={ref}>
      <div className='landing-terminal-bar'>
        <span className='landing-terminal-dots' aria-hidden='true'>
          <i />
          <i />
          <i />
        </span>
        <span className='landing-terminal-title'>terminal</span>
      </div>

      <div className='landing-terminal-body'>
        <div
          ref={outputRef}
          className='landing-terminal-output'
          role='region'
          aria-label='Scripted terminal demonstration'
          tabIndex={0}
        >
          {current.submitted ? (
            <p className='landing-terminal-request'>
              <span>❯</span> {TERMINAL_PROMPT}
            </p>
          ) : null}

          {TERMINAL_LINES.slice(0, current.lines).map(([tone, text]) => (
            <div key={text} className='landing-terminal-line' data-tone={tone}>
              {text}
            </div>
          ))}

          {current.submitted && !current.complete ? (
            <p className='landing-terminal-working'>
              <span className='landing-terminal-spinner' aria-hidden='true'>
                <i />
                <i />
                <i />
                <i />
              </span>
              Working… ({current.seconds}s)
            </p>
          ) : null}
        </div>

        {/* `aria-hidden` because the same prompt is already exposed as text in
            the output above once submitted; before that this is a typewriter
            animating and a screen reader has nothing to gain from it. */}
        <div className='landing-terminal-input' aria-hidden='true'>
          <span>❯</span>
          <span>
            {TERMINAL_PROMPT.slice(0, current.promptLength)}
            <i className='landing-terminal-caret' />
          </span>
        </div>
      </div>

      <div className='landing-terminal-status' aria-hidden='true'>
        <span>~/gateway</span>
        <span>channels</span>
        <span>{current.complete ? 'shadow mode on' : 'routing'}</span>
      </div>
    </div>
  )
}

function isSameFrame(a: TerminalFrame, b: TerminalFrame) {
  return (
    a.submitted === b.submitted &&
    a.promptLength === b.promptLength &&
    a.lines === b.lines &&
    a.seconds === b.seconds &&
    a.complete === b.complete
  )
}
