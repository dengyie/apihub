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
/**
 * The script the hero terminal plays.
 *
 * It is a fictional illustration of what the gateway does, English-only by
 * design: it is a code sample, not UI copy, and translating a shell session
 * would produce something that no longer looks like a terminal. It never shows
 * a live request.
 *
 * Tones map to the three colours the terminal is allowed to use — `command`
 * and `note` stay in ink, `add`/`success` carry the green channel, `remove`
 * the red one. Nothing else gets a colour, which is what keeps a 34-line
 * session readable instead of confetti.
 */
export const TERMINAL_PROMPT =
  'Add a second upstream for gpt-4o, split traffic across both, and keep serving when one goes down.'

export const TERMINAL_LINES = [
  ['note', 'Checking which models the new channel actually serves.'],
  ['command', '$ curl -s /api/channel/2/models | jq -r ".data[].id"'],
  ['muted', 'gpt-4o'],
  ['muted', 'gpt-4o-mini'],
  ['muted', 'text-embedding-3-large'],
  [
    'note',
    'Weights are per model, so gpt-4o can be split without moving the rest.',
  ],
  ['command', 'Patch channel priority'],
  ['remove', '- "priority": 0'],
  ['add', '+ "priority": 10,'],
  ['add', '+ "weight": 1,'],
  ['add', '+ "models": { "gpt-4o": { "enabled": true } }'],
  ['command', '$ curl -s -X POST /api/channel/test -d \'{ "id": 2 }\''],
  ['success', '  ✓ 3 models responded'],
  ['success', '  ✓ latency 142ms'],
  ['success', '  ✓ key accepted'],
  [
    'note',
    'Channel healthy. Enabling shadow mode before it takes real traffic.',
  ],
  ['command', 'Patch channel 2 — shadow'],
  ['add', '+ "shadow_mode": true'],
  ['command', '$ bun run test channel-routing'],
  ['success', '  ✓ falls back when the primary errors'],
  ['success', '  ✓ sticky session survives the retry'],
  ['success', '  ✓ shadow mode logs without failing over'],
  ['muted', '  Test Files  1 passed (1)'],
  ['muted', '       Tests  3 passed (3)'],
  ['muted', '    Duration  118ms'],
  [
    'note',
    'Done. Traffic is split, and a dead upstream now costs a retry instead of an outage.',
  ],
] as const

/** The prompt finishes typing at this point and the session begins. */
export const TERMINAL_SUBMIT_MS = 3000

// Bursts of output alternate with longer thinking pauses, so the session has a
// rhythm instead of scrolling at a constant rate. Notes weigh more than lines
// because the pauses are what make it read as work rather than as playback.
const LINE_WEIGHTS = TERMINAL_LINES.map(([tone], index) =>
  tone === 'note' ? 3 : 0.55 + (index % 3) * 0.35
)
const TOTAL_WEIGHT = LINE_WEIGHTS.reduce((sum, weight) => sum + weight, 0)

let accumulatedWeight = 0
export const TERMINAL_LINE_TIMES = LINE_WEIGHTS.map((weight) => {
  accumulatedWeight += weight
  return Math.round(
    TERMINAL_SUBMIT_MS + (accumulatedWeight / TOTAL_WEIGHT) * 10860
  )
})

export const TERMINAL_COMPLETE_MS =
  (TERMINAL_LINE_TIMES.at(-1) ?? TERMINAL_SUBMIT_MS) + 400

// A two-second hold on the finished output before the session restarts, so the
// last lines are readable before they are cleared.
export const TERMINAL_CYCLE_MS = TERMINAL_COMPLETE_MS + 2000

export type TerminalFrame = {
  submitted: boolean
  promptLength: number
  lines: number
  seconds: number
  complete: boolean
}

export function getTerminalFrame(
  elapsed: number,
  reducedMotion = false
): TerminalFrame {
  const time = reducedMotion
    ? TERMINAL_COMPLETE_MS
    : Math.max(0, elapsed) % TERMINAL_CYCLE_MS

  const submitted = time >= TERMINAL_SUBMIT_MS

  return {
    submitted,
    promptLength: submitted
      ? 0
      : Math.min(
          TERMINAL_PROMPT.length,
          Math.floor((Math.max(0, time - 300) / 2350) * TERMINAL_PROMPT.length)
        ),
    lines: TERMINAL_LINE_TIMES.filter((at) => time >= at).length,
    seconds: Math.floor(
      Math.max(0, Math.min(time, TERMINAL_COMPLETE_MS) - TERMINAL_SUBMIT_MS) /
        1000
    ),
    complete: time >= TERMINAL_COMPLETE_MS,
  }
}
