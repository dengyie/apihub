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

import { getChatFrameSandbox } from '../chat-links'

describe('Configured chat frame isolation', () => {
  it('preserves scripts and storage only for an external web origin', () => {
    const sandbox = getChatFrameSandbox(
      'https://chat.example.org',
      'https://gateway.example.org'
    )
    expect(sandbox).toContain('allow-scripts')
    expect(sandbox).toContain('allow-same-origin')
  })

  it.each([
    '/chat',
    'https://gateway.example.org/chat',
    'data:text/html,preview',
    'javascript:void(0)',
    'https://[invalid',
  ])('keeps %s isolated from the embedding document', (source) => {
    expect(
      getChatFrameSandbox(source, 'https://gateway.example.org')
    ).not.toContain('allow-same-origin')
  })
})
