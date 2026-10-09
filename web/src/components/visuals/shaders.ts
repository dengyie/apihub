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
// Procedural artwork inspired by the SnowAPI threads / metallic surfaces.
// No textures, remote resources or random runtime data are needed.
export const vertex = `#version 300 es
in vec2 position;
in vec2 uv;
out vec2 vUv;
void main() {
  vUv = uv;
  gl_Position = vec4(position, 0.0, 1.0);
}`

export const threads = `#version 300 es
precision highp float;
in vec2 vUv;
out vec4 fragColor;
uniform float uTime;
uniform vec2 uResolution;
uniform vec2 uPointer;
void main() {
  vec2 p = vUv;
  float ink = 0.0;
  float t = uTime * 0.16;
  for (int i = 0; i < 32; i++) {
    float n = float(i) / 31.0;
    float wave = sin(p.x * 5.4 + t + n * 1.9) * 0.12;
    wave += sin(p.x * 9.0 - t * 0.7 + n * 2.6) * 0.045;
    float y = 0.24 + n * 0.5 + wave * smoothstep(0.0, 0.7, p.x);
    y += (uPointer.y - 0.5) * sin(p.x * 3.14) * 0.08;
    float line = 1.0 - smoothstep(0.0007, 1.8 / uResolution.y, abs(p.y - y));
    ink = max(ink, line * (0.25 + 0.55 * sin(n * 3.14)));
  }
  float edge = smoothstep(0.0, 0.14, p.x) * (1.0 - smoothstep(0.8, 1.0, p.x));
  fragColor = vec4(vec3(0.5), ink * edge);
}`

export const metal = `#version 300 es
precision highp float;
in vec2 vUv;
out vec4 fragColor;
uniform float uTime;
uniform vec2 uResolution;
uniform vec2 uPointer;
void main() {
  vec2 p = (vUv - 0.5) * 2.0;
  p.x *= uResolution.x / uResolution.y;
  p += (uPointer - 0.5) * 0.07;
  float t = uTime * 0.14;
  float angle = atan(p.y, p.x);
  float radius = length(p);
  float contour = 0.6 + sin(angle * 3.0 + t) * 0.045 + cos(angle * 5.0 - t) * 0.025;
  float d = radius - contour;
  float ring = 1.0 - smoothstep(0.1, 0.115, abs(d));
  float normal = d / 0.115;
  float reflection = sin(normal * 5.0 + sin(angle * 2.0 + t) * 2.5);
  float chrome = 0.15 + 0.8 * pow(max(0.0, reflection), 3.0);
  chrome += 0.3 * pow(max(0.0, cos(angle - 0.7)), 12.0);
  chrome += 0.13 * (1.0 - abs(normal));
  vec3 tint = mix(vec3(0.57, 0.61, 0.7), vec3(1.0, 0.98, 0.92), chrome);
  vec3 color = tint * chrome * ring;
  float glow = exp(-abs(d) * 16.0) * 0.07;
  fragColor = vec4(color + glow, max(ring, glow));
}`
