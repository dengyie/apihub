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
import { describe, expect, test } from 'vitest'

import {
  maskProxyUrl,
  getMultiKeyStatusConfig,
  getMultiKeyConfirmMessage,
  isDestructiveAction,
} from '../multi-key-utils'

describe('multi-key utils', () => {
  describe('maskProxyUrl', () => {
    test('masks passwords in socks5 URLs', () => {
      expect(maskProxyUrl('socks5://user:secret123@127.0.0.1:2080')).toBe(
        'socks5://user:***@127.0.0.1:2080'
      )
    })

    test('masks passwords in http URLs', () => {
      expect(maskProxyUrl('http://admin:pass@proxy.example.com:8080')).toBe(
        'http://admin:***@proxy.example.com:8080'
      )
      expect(maskProxyUrl('http://admin:pass@proxy.example.com:8080/')).toBe(
        'http://admin:***@proxy.example.com:8080/'
      )
    })

    test('leaves proxy without password unchanged', () => {
      expect(maskProxyUrl('socks5://127.0.0.1:1080')).toBe(
        'socks5://127.0.0.1:1080'
      )
      expect(maskProxyUrl('socks5://user@127.0.0.1:1080')).toBe(
        'socks5://user@127.0.0.1:1080'
      )
    })

    test('handles empty or undefined proxy', () => {
      expect(maskProxyUrl('')).toBe('')
      expect(maskProxyUrl(undefined)).toBe('')
    })
  })

  describe('isDestructiveAction', () => {
    test('identifies destructive actions correctly', () => {
      expect(isDestructiveAction({ type: 'delete', keyIndex: 0 })).toBe(true)
      expect(isDestructiveAction({ type: 'delete-disabled' })).toBe(true)
      expect(isDestructiveAction({ type: 'disable-all' })).toBe(true)
      expect(isDestructiveAction({ type: 'enable', keyIndex: 0 })).toBe(false)
      expect(isDestructiveAction({ type: 'disable', keyIndex: 0 })).toBe(false)
      expect(isDestructiveAction(null)).toBe(false)
    })
  })

  describe('getMultiKeyStatusConfig', () => {
    test('returns correct configuration for known statuses', () => {
      expect(getMultiKeyStatusConfig(1)).toEqual({
        variant: 'success',
        label: 'Enabled',
      })
      expect(getMultiKeyStatusConfig(2)).toEqual({
        variant: 'neutral',
        label: 'Manual Disabled',
      })
      expect(getMultiKeyStatusConfig(3)).toEqual({
        variant: 'danger',
        label: 'Auto Disabled',
      })
    })

    test('returns fallback configuration for unknown statuses', () => {
      expect(getMultiKeyStatusConfig(999)).toEqual({
        variant: 'neutral',
        label: 'Unknown',
      })
    })
  })

  describe('getMultiKeyConfirmMessage', () => {
    test('returns correct message for each action type', () => {
      expect(getMultiKeyConfirmMessage({ type: 'delete', keyIndex: 0 })).toBe(
        'Are you sure you want to delete this key? This action cannot be undone.'
      )
      expect(getMultiKeyConfirmMessage({ type: 'enable', keyIndex: 0 })).toBe(
        'Enable this key?'
      )
      expect(getMultiKeyConfirmMessage({ type: 'disable', keyIndex: 0 })).toBe(
        'Disable this key?'
      )
      expect(getMultiKeyConfirmMessage({ type: 'enable-all' })).toBe(
        'Are you sure you want to enable all keys?'
      )
      expect(getMultiKeyConfirmMessage({ type: 'disable-all' })).toBe(
        'Are you sure you want to disable all enabled keys?'
      )
      expect(getMultiKeyConfirmMessage({ type: 'delete-disabled' })).toBe(
        'Are you sure you want to delete all auto-disabled keys? This action cannot be undone.'
      )
    })

    test('returns empty string for null action', () => {
      expect(getMultiKeyConfirmMessage(null)).toBe('')
    })
  })
})
