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

import { useIsMobile } from '@/hooks/use-mobile'
import {
  useVisibleAnimationFrame,
  useVisibleMotion,
} from '@/hooks/use-visible-motion'
import { cn } from '@/lib/utils'

import type { createShaderScene } from './shader-scene'

type ShaderArtworkProps = {
  variant: 'threads' | 'metal'
  className?: string
  paused?: boolean
}

export function ShaderArtwork(props: ShaderArtworkProps) {
  const mobile = useIsMobile()
  const disabled = props.variant === 'metal' && mobile
  const { ref, playing, reducedMotion } = useVisibleMotion<HTMLDivElement>(
    disabled || props.paused
  )
  const canvas = useRef<HTMLCanvasElement>(null)
  const scene = useRef<ReturnType<typeof createShaderScene>>(null)
  const [mounted, setMounted] = useState(false)
  const [ready, setReady] = useState(false)
  const [lost, setLost] = useState(false)

  useEffect(() => {
    if (playing) setMounted(true)
  }, [playing])

  useEffect(() => {
    if (!mounted || disabled || reducedMotion || lost || !canvas.current) return
    let cancelled = false
    const element = canvas.current
    const onContextLost = () => {
      setLost(true)
      setReady(false)
    }
    element.addEventListener('webglcontextlost', onContextLost)
    void import('./shader-scene')
      .then(({ createShaderScene: create }) => {
        if (cancelled) return
        try {
          scene.current = create(element, props.variant)
          setReady(Boolean(scene.current))
        } catch {
          // The static artwork is already painted below the canvas.
          setReady(false)
        }
      })
      .catch(() => {
        if (!cancelled) setReady(false)
      })
    return () => {
      cancelled = true
      element.removeEventListener('webglcontextlost', onContextLost)
      scene.current?.dispose()
      scene.current = null
      setReady(false)
    }
  }, [mounted, disabled, reducedMotion, lost, props.variant])

  useVisibleAnimationFrame(playing && ready, (elapsed) =>
    scene.current?.draw(elapsed)
  )

  return (
    <div
      ref={ref}
      aria-hidden='true'
      className={cn(
        'shader-artwork',
        `shader-artwork-${props.variant}`,
        props.className
      )}
      data-renderer={ready ? 'webgl' : 'static'}
      data-playing={playing && ready}
      onPointerMove={(event) => {
        if (!playing) return
        const rect = event.currentTarget.getBoundingClientRect()
        scene.current?.point(
          (event.clientX - rect.left) / rect.width,
          1 - (event.clientY - rect.top) / rect.height
        )
      }}
      onPointerLeave={() => scene.current?.point(0.5, 0.5)}
    >
      <div className='shader-fallback'>
        {props.variant === 'threads' ? (
          <svg viewBox='0 0 800 400' preserveAspectRatio='none'>
            {Array.from({ length: 24 }, (_, i) => (
              <path
                key={i}
                d={`M-50 ${100 + i * 7} C220 ${50 + i * 9} 350 ${370 - i * 7} 850 ${150 + i * 6}`}
              />
            ))}
          </svg>
        ) : (
          <div className='shader-metal-ring' />
        )}
      </div>
      {/* A disposed WebGL context cannot be reused. Remount the canvas when
          returning from a temporary static fallback or changing the shader. */}
      {!disabled && !reducedMotion && !lost && (
        <canvas key={props.variant} ref={canvas} />
      )}
    </div>
  )
}
