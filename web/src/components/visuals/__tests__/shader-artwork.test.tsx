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
import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ShaderArtwork } from '../shader-artwork'

let reduced = false
let intersect: IntersectionObserverCallback
const mediaChanges = new Map<string, () => void>()

beforeEach(() => {
  reduced = false
  mediaChanges.clear()
  vi.stubGlobal('innerWidth', 1280)
  vi.stubGlobal('matchMedia', (media: string) => ({
    media,
    get matches() {
      return media.includes('prefers-reduced-motion') && reduced
    },
    addEventListener: vi.fn((_event: string, listener: () => void) => {
      mediaChanges.set(media, listener)
    }),
    removeEventListener: vi.fn(() => mediaChanges.delete(media)),
  }))
  vi.stubGlobal(
    'IntersectionObserver',
    class {
      constructor(callback: IntersectionObserverCallback) {
        intersect = callback
      }
      observe() {}
      disconnect() {}
    }
  )
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function makeVisible() {
  act(() => {
    intersect(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      {} as IntersectionObserver
    )
  })
}

describe('shader artwork fallbacks', () => {
  it('keeps the static artwork and does not request WebGL when reduced motion is enabled', () => {
    reduced = true
    const context = vi.spyOn(HTMLCanvasElement.prototype, 'getContext')
    const { container } = render(<ShaderArtwork variant='threads' />)
    makeVisible()
    expect(context).not.toHaveBeenCalled()
    expect(container.querySelector('.shader-artwork')).toHaveAttribute(
      'data-renderer',
      'static'
    )
    expect(container.querySelector('.shader-artwork')).toHaveAttribute(
      'data-playing',
      'false'
    )
    expect(container.querySelector('.shader-fallback svg')).toBeInTheDocument()
  })

  it('uses a static metal illustration on mobile without allocating a GPU context', () => {
    vi.stubGlobal('innerWidth', 390)
    const context = vi.spyOn(HTMLCanvasElement.prototype, 'getContext')
    const { container } = render(<ShaderArtwork variant='metal' />)
    makeVisible()
    expect(context).not.toHaveBeenCalled()
    expect(container.querySelector('.shader-artwork')).toHaveAttribute(
      'data-renderer',
      'static'
    )
    expect(container.querySelector('.shader-artwork')).toHaveAttribute(
      'data-playing',
      'false'
    )
  })

  it('keeps the fallback visible when the browser denies a WebGL context', async () => {
    const context = vi
      .spyOn(HTMLCanvasElement.prototype, 'getContext')
      .mockReturnValue(null)
    const { container } = render(<ShaderArtwork variant='threads' />)
    makeVisible()
    await waitFor(() =>
      expect(context).toHaveBeenCalledWith('webgl2', {
        alpha: true,
        antialias: false,
      })
    )
    expect(container.querySelector('.shader-artwork')).toHaveAttribute(
      'data-renderer',
      'static'
    )
    expect(container.querySelector('.shader-artwork')).toHaveAttribute(
      'data-playing',
      'false'
    )
    expect(container.querySelector('.shader-fallback svg')).toBeInTheDocument()
  })

  it.each(['mobile', 'reduced motion'] as const)(
    'requests a fresh canvas after returning from %s to desktop animation',
    async (reason) => {
      const context = vi
        .spyOn(HTMLCanvasElement.prototype, 'getContext')
        .mockReturnValue(null)
      const { container } = render(<ShaderArtwork variant='metal' />)
      makeVisible()
      await waitFor(() => expect(context).toHaveBeenCalled())
      const initialCanvas = container.querySelector('canvas')
      context.mockClear()

      act(() => {
        if (reason === 'mobile') {
          vi.stubGlobal('innerWidth', 390)
          mediaChanges.get('(max-width: 767px)')?.()
        } else {
          reduced = true
          mediaChanges.get('(prefers-reduced-motion: reduce)')?.()
        }
      })
      expect(container.querySelector('.shader-artwork')).toHaveAttribute(
        'data-playing',
        'false'
      )
      expect(container.querySelector('canvas')).not.toBeInTheDocument()
      expect(context).not.toHaveBeenCalled()

      act(() => {
        if (reason === 'mobile') {
          vi.stubGlobal('innerWidth', 1280)
          mediaChanges.get('(max-width: 767px)')?.()
        } else {
          reduced = false
          mediaChanges.get('(prefers-reduced-motion: reduce)')?.()
        }
      })
      await waitFor(() => expect(context).toHaveBeenCalled())
      expect(container.querySelector('canvas')).not.toBe(initialCanvas)
      expect(context.mock.instances[0]).toBe(container.querySelector('canvas'))
    }
  )
})
