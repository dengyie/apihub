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
import { act, cleanup, render, screen } from '@testing-library/react'
import { useRef, useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  useVisibleAnimationFrame,
  useVisibleMotion,
} from '../use-visible-motion'

/**
 * The hook watches a real element through an IntersectionObserver, so these
 * assert what a caller can observe — `playing` and `reducedMotion` as the
 * surface renders them — never the observer's own state.
 */
function Harness({ paused = false }: { paused?: boolean }) {
  const { ref, reducedMotion, playing } =
    useVisibleMotion<HTMLDivElement>(paused)
  return (
    <div
      ref={ref}
      data-testid='surface'
      data-playing={String(playing)}
      data-reduced-motion={String(reducedMotion)}
    />
  )
}

type IntersectionCallback = (entries: { isIntersecting: boolean }[]) => void

let intersect: IntersectionCallback = () => {}
let motionChanged = false
let motionListeners: ((event: Event) => void)[] = []
const queuedFrames = new Map<number, FrameRequestCallback>()
let hidden = false
let clock = 0
let nextFrameId = 0

function playing() {
  return screen.getByTestId('surface').dataset.playing
}

function prefersReducedMotion() {
  return screen.getByTestId('surface').dataset.reducedMotion
}

/** Runs the frame callbacks queued so far at `now`. */
function advance(now: number) {
  const due = [...queuedFrames.values()]
  queuedFrames.clear()
  act(() => {
    for (const callback of due) callback(now)
  })
}

function scrollIntoView(isIntersecting: boolean) {
  act(() => {
    intersect([{ isIntersecting }])
  })
}

function setHidden(next: boolean) {
  act(() => {
    hidden = next
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

function setReducedMotion(next: boolean) {
  act(() => {
    motionChanged = next
    for (const listener of motionListeners) listener(new Event('change'))
  })
}

beforeEach(() => {
  motionChanged = false
  motionListeners = []
  queuedFrames.clear()
  nextFrameId = 0
  hidden = false
  clock = 0

  vi.stubGlobal(
    'IntersectionObserver',
    class {
      constructor(callback: IntersectionCallback) {
        intersect = callback
      }
      observe() {}
      disconnect() {}
      unobserve() {}
      takeRecords() {
        return []
      }
      root = null
      rootMargin = ''
      thresholds = []
    }
  )

  vi.stubGlobal(
    'matchMedia',
    (query: string) =>
      ({
        media: query,
        get matches() {
          return query.includes('prefers-reduced-motion') && motionChanged
        },
        addEventListener: (type: string, listener: (event: Event) => void) => {
          if (type === 'change') motionListeners.push(listener)
        },
        removeEventListener: (
          type: string,
          listener: (event: Event) => void
        ) => {
          if (type !== 'change') return
          const index = motionListeners.indexOf(listener)
          if (index >= 0) motionListeners.splice(index, 1)
        },
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
        onchange: null,
      }) as unknown as MediaQueryList
  )

  vi.stubGlobal('performance', { now: () => clock })
  vi.stubGlobal(
    'requestAnimationFrame',
    vi.fn((callback: FrameRequestCallback) => {
      nextFrameId += 1
      queuedFrames.set(nextFrameId, callback)
      return nextFrameId
    })
  )
  // Cancelling has to actually drop the frame, otherwise a stale loop would
  // keep queueing work behind the new one and the test could not tell.
  vi.stubGlobal(
    'cancelAnimationFrame',
    vi.fn((id: number) => {
      queuedFrames.delete(id)
    })
  )

  Object.defineProperty(document, 'hidden', {
    configurable: true,
    get: () => hidden,
  })
})

afterEach(() => {
  cleanup()
  queuedFrames.clear()
  vi.unstubAllGlobals()
})

describe('useVisibleMotion', () => {
  it('holds still until the surface has been scrolled into view', () => {
    render(<Harness />)

    expect(playing()).toBe('false')
  })

  it('plays while the surface is on screen', () => {
    render(<Harness />)

    scrollIntoView(true)

    expect(playing()).toBe('true')
  })

  it('holds still again when the surface scrolls back out', () => {
    render(<Harness />)

    scrollIntoView(true)
    scrollIntoView(false)

    expect(playing()).toBe('false')
  })

  it('holds still while the tab is in the background', () => {
    render(<Harness />)

    scrollIntoView(true)
    setHidden(true)

    expect(playing()).toBe('false')
  })

  it('plays again when the tab comes back', () => {
    render(<Harness />)

    scrollIntoView(true)
    setHidden(true)
    setHidden(false)

    expect(playing()).toBe('true')
  })

  it('stops and reports the preference when reduced motion is turned on', () => {
    render(<Harness />)

    scrollIntoView(true)
    setReducedMotion(true)

    expect(playing()).toBe('false')
    expect(prefersReducedMotion()).toBe('true')
  })

  it('starts held back when the user already prefers reduced motion', () => {
    motionChanged = true
    render(<Harness />)

    scrollIntoView(true)

    expect(playing()).toBe('false')
    expect(prefersReducedMotion()).toBe('true')
  })

  it('resumes when reduced motion is turned back off', () => {
    render(<Harness />)

    scrollIntoView(true)
    setReducedMotion(true)
    setReducedMotion(false)

    expect(playing()).toBe('true')
  })

  it('stays held when the caller pauses it', () => {
    render(<Harness paused />)

    scrollIntoView(true)

    expect(playing()).toBe('false')
  })

  it('stops observing and listening on unmount', () => {
    const disconnect = vi.fn()
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(callback: IntersectionCallback) {
          intersect = callback
        }
        observe() {}
        disconnect = disconnect
        unobserve() {}
        takeRecords() {
          return []
        }
        root = null
        rootMargin = ''
        thresholds = []
      }
    )

    const { unmount } = render(<Harness />)
    unmount()

    expect(disconnect).toHaveBeenCalled()
    expect(motionListeners).toHaveLength(0)
  })

  it('observes the element when it mounts after the initial render', () => {
    function DelayedHarness({ show }: { show: boolean }) {
      const { ref, playing } = useVisibleMotion<HTMLDivElement>()
      return (
        <div>
          {show ? (
            <div
              ref={ref}
              data-testid='surface'
              data-playing={String(playing)}
            />
          ) : (
            <span data-testid='fallback'>loading</span>
          )}
        </div>
      )
    }

    const { rerender } = render(<DelayedHarness show={false} />)
    expect(screen.getByTestId('fallback')).toBeDefined()

    rerender(<DelayedHarness show />)
    scrollIntoView(true)
    expect(playing()).toBe('true')
  })
})

function Clock({ playing = false }: { playing?: boolean }) {
  const [drawn, setDrawn] = useState<number | null>(null)
  useVisibleAnimationFrame(playing, (elapsed) => setDrawn(elapsed))
  return <span data-testid='elapsed'>{drawn ?? 'none'}</span>
}

describe('useVisibleAnimationFrame', () => {
  it('never requests a frame while held', () => {
    render(<Clock />)

    expect(queuedFrames.size).toBe(0)
    expect(screen.getByTestId('elapsed').textContent).toBe('none')
  })

  it('skips drawing frames that arrive faster than 30fps', () => {
    render(<Clock playing />)

    advance(0)
    // Under one 30fps step, so the frame ran without drawing.
    advance(16)
    expect(screen.getByTestId('elapsed').textContent).toBe('0')

    advance(40)
    expect(screen.getByTestId('elapsed').textContent).toBe('40')
  })

  it('advances by the clock, not by how long the tab was away', () => {
    render(<Clock playing />)

    advance(0)
    advance(40)
    expect(screen.getByTestId('elapsed').textContent).toBe('40')

    // A tab left in the background for half a minute returns on one frame. The
    // script has to pick up where it stopped, not replay the whole gap.
    advance(30_040)
    expect(screen.getByTestId('elapsed').textContent).toBe('140')
  })

  it('uses the latest draw callback without restarting the clock', () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender } = render(<Draw playing callback={first} label='first' />)

    advance(0)
    expect(first).toHaveBeenCalledTimes(1)

    rerender(<Draw playing callback={second} label='second' />)
    advance(40)

    expect(first).toHaveBeenCalledTimes(1)
    expect(second).toHaveBeenCalledWith(40)
    expect(screen.getByTestId('drawn').textContent).toBe('second')
  })

  it('stops requesting frames once it is held again', () => {
    const { rerender } = render(<Clock playing />)

    expect(queuedFrames.size).toBe(1)

    advance(0)
    advance(40)
    expect(screen.getByTestId('elapsed').textContent).toBe('40')

    rerender(<Clock playing={false} />)
    expect(queuedFrames.size).toBe(0)

    // Resuming starts exactly one fresh loop rather than leaving the held one
    // running alongside it, and the clock carries on from 40 instead of
    // restarting from zero.
    rerender(<Clock playing />)
    expect(queuedFrames.size).toBe(1)

    advance(80)
    expect(screen.getByTestId('elapsed').textContent).toBe('120')
  })
})

function Draw({
  playing,
  callback,
  label,
}: {
  playing: boolean
  callback: (elapsed: number) => void
  label: string
}) {
  const last = useRef(0)
  useVisibleAnimationFrame(playing, (elapsed) => {
    last.current = elapsed
    callback(elapsed)
  })
  return <span data-testid='drawn'>{label}</span>
}
