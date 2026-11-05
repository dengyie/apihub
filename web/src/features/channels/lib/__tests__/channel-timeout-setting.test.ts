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
import { assert, describe, expect, test } from 'vitest'

import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
} from '../channel-form'
import { getChannelConfigurationSection } from '../channel-configuration'
import { channelSchema } from '../../types'

function makeChannel(setting: string) {
  return channelSchema.parse({
    id: 1,
    name: 'timeout channel',
    key: '',
    type: 1,
    status: 1,
    created_time: 0,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    setting,
  })
}

describe('channel first-token / header timeout settings', () => {
  test('unset values are omitted so unchanged channels keep their JSON', () => {
    const setting = JSON.parse(buildSettingJSON(CHANNEL_FORM_DEFAULT_VALUES))
    expect(setting).not.toHaveProperty('ttft_timeout_ms')
    expect(setting).not.toHaveProperty('header_timeout_ms')
  })

  test('configured values are written to the setting JSON', () => {
    const setting = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        ttft_timeout_ms: 30000,
        header_timeout_ms: 15000,
      })
    )
    expect(setting.ttft_timeout_ms).toBe(30000)
    expect(setting.header_timeout_ms).toBe(15000)
  })

  test('zero is treated as inherit rather than an explicit timeout', () => {
    const setting = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        ttft_timeout_ms: 0,
        header_timeout_ms: 0,
      })
    )
    expect(setting).not.toHaveProperty('ttft_timeout_ms')
    expect(setting).not.toHaveProperty('header_timeout_ms')
  })

  test('existing setting is parsed back into form values and survives a round trip', () => {
    const channel = makeChannel(
      JSON.stringify({ ttft_timeout_ms: 45000, header_timeout_ms: 20000 })
    )
    const values = transformChannelToFormDefaults(channel)
    expect(values.ttft_timeout_ms).toBe(45000)
    expect(values.header_timeout_ms).toBe(20000)

    const payload = transformFormDataToUpdatePayload(values, channel.id)
    assert(typeof payload.setting === 'string')
    const roundTripped = JSON.parse(payload.setting)
    expect(roundTripped.ttft_timeout_ms).toBe(45000)
    expect(roundTripped.header_timeout_ms).toBe(20000)
  })

  test('legacy setting without the keys yields undefined, not zero', () => {
    const values = transformChannelToFormDefaults(
      makeChannel(JSON.stringify({ force_format: true }))
    )
    expect(values.ttft_timeout_ms).toBeUndefined()
    expect(values.header_timeout_ms).toBeUndefined()
  })
})

const validFormValues = (overrides: Record<string, unknown> = {}) => ({
  ...CHANNEL_FORM_DEFAULT_VALUES,
  name: 'timeout channel',
  type: 1,
  models: 'gpt-4o',
  status: 1,
  ...overrides,
})

describe('channel timeout form validation', () => {
  // 与后端 dto.MinChannelTimeoutMs / MaxChannelTimeoutMs 对齐：表单能过的值
  // 服务端必须也能过，否则用户拿到的是一个英文的服务端错误。
  test('accepts 0 and [100, 1800000] only', () => {
    const accepts = (value: number | undefined) =>
      channelFormSchema.safeParse(
        validFormValues({ ttft_timeout_ms: value })
      ).success

    expect(accepts(undefined)).toBe(true)
    expect(accepts(0)).toBe(true)
    expect(accepts(100)).toBe(true)
    expect(accepts(30000)).toBe(true)
    expect(accepts(1_800_000)).toBe(true)

    expect(accepts(99)).toBe(false)
    expect(accepts(1_800_001)).toBe(false)
    expect(accepts(-1)).toBe(false)
    expect(accepts(1500.5)).toBe(false)
  })

  // 校验失败时抽屉要跳到正确的配置区块：未登记会落到兜底的 connection。
  test('both fields belong to the routing section', () => {
    expect(getChannelConfigurationSection('ttft_timeout_ms')).toBe('routing')
    expect(getChannelConfigurationSection('header_timeout_ms')).toBe('routing')
  })
})
