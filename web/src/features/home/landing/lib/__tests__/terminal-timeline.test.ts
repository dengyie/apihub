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
import { describe, expect, it } from 'vitest'

import {
  getTerminalFrame,
  TERMINAL_COMPLETE_MS,
  TERMINAL_CYCLE_MS,
  TERMINAL_LINE_TIMES,
  TERMINAL_LINES,
  TERMINAL_PROMPT,
  TERMINAL_SUBMIT_MS,
} from '../terminal-timeline'

describe('the hero terminal script', () => {
  it('starts on an empty session with the prompt not yet typed', () => {
    const frame = getTerminalFrame(0)

    expect(frame.submitted).toBe(false)
    expect(frame.lines).toBe(0)
    expect(frame.promptLength).toBe(0)
    expect(frame.complete).toBe(false)
  })

  it('never asks for a negative slice of the prompt', () => {
    // The typing starts 300ms in, so every frame before that used to compute a
    // negative length and the hero rendered the prompt minus its last words.
    for (let elapsed = 0; elapsed < TERMINAL_SUBMIT_MS; elapsed += 25) {
      const frame = getTerminalFrame(elapsed)
      expect(frame.promptLength).toBeGreaterThanOrEqual(0)
      expect(frame.promptLength).toBeLessThanOrEqual(TERMINAL_PROMPT.length)
    }
  })

  it('never renders more lines than the script has', () => {
    for (let elapsed = 0; elapsed < TERMINAL_CYCLE_MS; elapsed += 97) {
      expect(getTerminalFrame(elapsed).lines).toBeLessThanOrEqual(
        TERMINAL_LINES.length
      )
    }
  })

  it('types the prompt character by character, then submits it', () => {
    const beforeSubmit = getTerminalFrame(TERMINAL_SUBMIT_MS - 1)
    expect(beforeSubmit.submitted).toBe(false)
    expect(beforeSubmit.promptLength).toBe(TERMINAL_PROMPT.length)

    const afterSubmit = getTerminalFrame(TERMINAL_SUBMIT_MS)
    expect(afterSubmit.submitted).toBe(true)
    expect(afterSubmit.promptLength).toBe(0)
  })

  it('adds output lines one at a time and never removes one', () => {
    let previous = 0
    for (
      let elapsed = TERMINAL_SUBMIT_MS;
      elapsed < TERMINAL_COMPLETE_MS;
      elapsed += 50
    ) {
      const { lines } = getTerminalFrame(elapsed)
      expect(lines).toBeGreaterThanOrEqual(previous)
      previous = lines
    }
    expect(previous).toBe(TERMINAL_LINES.length)
  })

  it('finishes on the complete output', () => {
    const frame = getTerminalFrame(TERMINAL_COMPLETE_MS)

    expect(frame.complete).toBe(true)
    expect(frame.lines).toBe(TERMINAL_LINES.length)
    expect(frame.seconds).toBe(
      Math.floor((TERMINAL_COMPLETE_MS - TERMINAL_SUBMIT_MS) / 1000)
    )
  })

  it('holds the finished output before restarting', () => {
    expect(getTerminalFrame(TERMINAL_CYCLE_MS - 1).complete).toBe(true)
  })

  it('restarts the session once the cycle elapses', () => {
    const frame = getTerminalFrame(TERMINAL_CYCLE_MS)

    expect(frame).toEqual(getTerminalFrame(0))
  })

  it('treats a negative clock as the start of the cycle', () => {
    expect(getTerminalFrame(-5000)).toEqual(getTerminalFrame(0))
  })

  it('shows the finished output when motion is reduced', () => {
    for (const elapsed of [0, 1500, TERMINAL_SUBMIT_MS, 9000]) {
      expect(getTerminalFrame(elapsed, true)).toEqual(
        getTerminalFrame(TERMINAL_COMPLETE_MS, true)
      )
    }
  })

  it('counts the seconds spent on the request, never negative', () => {
    expect(getTerminalFrame(0).seconds).toBe(0)
    expect(getTerminalFrame(TERMINAL_SUBMIT_MS + 2500).seconds).toBe(2)
    expect(getTerminalFrame(TERMINAL_CYCLE_MS * 3).seconds).toBe(0)
  })
})

describe('the timing constants', () => {
  it('gives every line its own moment, after the prompt is submitted', () => {
    for (const at of TERMINAL_LINE_TIMES) {
      expect(at).toBeGreaterThan(TERMINAL_SUBMIT_MS)
    }
    const sorted = [...TERMINAL_LINE_TIMES].sort((a, b) => a - b)
    expect(TERMINAL_LINE_TIMES).toEqual(sorted)
    expect(new Set(TERMINAL_LINE_TIMES).size).toBe(TERMINAL_LINE_TIMES.length)
  })

  it('paces notes more slowly than the output they introduce', () => {
    const [noteTone, noteAt] = [TERMINAL_LINES[0][0], TERMINAL_LINE_TIMES[0]]
    expect(noteTone).toBe('note')

    const [mutedTone, mutedAt] = [TERMINAL_LINES[1][0], TERMINAL_LINE_TIMES[1]]
    expect(mutedTone).toBe('command')

    expect(noteAt).toBeLessThan(mutedAt)
  })

  it('holds the finished session for two seconds before looping', () => {
    expect(TERMINAL_CYCLE_MS - TERMINAL_COMPLETE_MS).toBe(2000)
  })
})
