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
import { Mesh, Program, Renderer, Triangle } from 'ogl'

import { metal, threads, vertex } from './shaders'

export function createShaderScene(
  canvas: HTMLCanvasElement,
  variant: 'threads' | 'metal'
) {
  // The black metal surface needs WebGL 2. A static CSS artwork stays visible
  // if the browser denies a GPU context or cannot link the shader.
  const container = canvas.parentElement
  if (!container) return null
  const gl = canvas.getContext('webgl2', { alpha: true, antialias: false })
  if (!gl) return null
  const renderer = new Renderer({
    canvas,
    alpha: true,
    antialias: false,
    webgl: 2,
  })
  const geometry = new Triangle(renderer.gl)
  const program = new Program(renderer.gl, {
    vertex,
    fragment: variant === 'metal' ? metal : threads,
    transparent: true,
    depthTest: false,
    depthWrite: false,
    uniforms: {
      uTime: { value: 0 },
      uResolution: { value: [1, 1] },
      uPointer: { value: [0.5, 0.5] },
    },
  })
  if (!gl.getProgramParameter(program.program, gl.LINK_STATUS)) {
    geometry.remove()
    program.remove()
    gl.getExtension('WEBGL_lose_context')?.loseContext()
    return null
  }
  const mesh = new Mesh(renderer.gl, { geometry, program })
  const target = [0.5, 0.5]
  let elapsed = 0
  let disposed = false
  const draw = (time: number) => {
    if (disposed || gl.isContextLost()) return
    elapsed = time
    const pointer = program.uniforms.uPointer.value as number[]
    pointer[0] += (target[0] - pointer[0]) * 0.08
    pointer[1] += (target[1] - pointer[1]) * 0.08
    program.uniforms.uTime.value = time / 1000
    renderer.render({ scene: mesh })
  }
  const resize = () => {
    if (disposed) return
    const { width, height } = container.getBoundingClientRect()
    if (!width || !height) return
    renderer.dpr = Math.min(
      window.devicePixelRatio || 1,
      1.5,
      1800 / Math.max(width, height)
    )
    renderer.setSize(width, height)
    program.uniforms.uResolution.value = [canvas.width, canvas.height]
    if (!document.hidden) draw(elapsed)
  }
  const observer = new ResizeObserver(resize)
  observer.observe(container)
  resize()
  return {
    draw,
    point(x: number, y: number) {
      target[0] = x
      target[1] = y
    },
    dispose() {
      disposed = true
      observer.disconnect()
      geometry.remove()
      program.remove()
      gl.getExtension('WEBGL_lose_context')?.loseContext()
    },
  }
}
