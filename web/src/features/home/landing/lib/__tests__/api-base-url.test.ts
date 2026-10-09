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

import { resolveApiBaseUrl } from '../api-base-url'

describe('homepage API address', () => {
  it.each([
    ['https://api.example.test', 'https://api.example.test/v1'],
    ['https://api.example.test/proxy/', 'https://api.example.test/proxy/v1'],
    ['https://api.example.test/v1/', 'https://api.example.test/v1'],
    [
      'https://api.example.test/proxy?debug=true#test',
      'https://api.example.test/proxy/v1',
    ],
    [undefined, 'http://localhost:4175/v1'],
    ['javascript:alert(1)', 'http://localhost:4175/v1'],
    ['https://user:password@example.test', 'http://localhost:4175/v1'],
  ])(
    'resolves %s without duplicating the API prefix or copying credentials',
    (value, expected) => {
      expect(resolveApiBaseUrl(value, 'http://localhost:4175')).toBe(expected)
    }
  )
})
