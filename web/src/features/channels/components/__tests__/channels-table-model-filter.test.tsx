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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import type { Channel } from '../../types'
import { ChannelsProvider } from '../channels-provider'
import { ChannelsTable } from '../channels-table'

const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} } },
  initAsync: false,
})

const clients: QueryClient[] = []

function testChannel(id: number, name: string, models: string): Channel {
  return {
    id,
    type: 1,
    key: 'sk-test',
    status: 1,
    name,
    created_time: 0,
    test_time: 0,
    response_time: 0,
    other: '',
    balance: 0,
    balance_updated_time: 0,
    models,
    group: 'default',
    used_quota: 0,
    other_info: '',
    remark: '',
    max_input_tokens: 0,
    channel_info: {
      is_multi_key: false,
      multi_key_size: 0,
      multi_key_polling_index: 0,
      multi_key_mode: 'random',
    },
    settings: '{}',
  }
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
})

afterEach(() => {
  cleanup()
  localStorage.clear()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
})

async function setupChannelsTable(options?: {
  enabledModels?: string[]
  items?: Channel[]
  initialUrl?: string
}) {
  const enabledModels = options?.enabledModels ?? ['gpt-4o', 'claude-3-5-sonnet', 'deepseek-chat']
  const items = options?.items ?? [testChannel(1, 'openai-prod', 'gpt-4o,gpt-4o-mini')]

  const getSpy = vi.spyOn(api, 'get').mockImplementation(async (url, _config) => {
    if (url === '/api/channel/models_enabled') {
      return {
        data: {
          success: true,
          data: enabledModels,
        },
      }
    }
    if (url === '/api/channel/search') {
      return {
        data: {
          success: true,
          data: { items, total: items.length, type_counts: {} },
        },
      }
    }
    if (url === '/api/channel') {
      return {
        data: {
          success: true,
          data: { items, total: items.length, type_counts: {} },
        },
      }
    }
    if (url === '/api/group') {
      return { data: { success: true, data: ['default', 'vip'] } }
    }
    return { data: { success: true, data: [] } }
  })

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)

  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const channelsRoute = createRoute({
    getParentRoute: () => auth,
    path: 'channels/',
    validateSearch: (search: Record<string, unknown>) => search,
    component: () => (
      <ChannelsProvider>
        <ChannelsTable />
      </ChannelsProvider>
    ),
  })

  const initialEntry = options?.initialUrl ?? '/channels/'
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([channelsRoute])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  await router.load()

  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  )

  expect((await screen.findAllByText('openai-prod')).length).toBeGreaterThan(0)
  return { getSpy, router }
}

describe('ChannelsTable Model Filter Combobox', () => {
  it('renders a Combobox input for model filter with placeholder and combobox role', async () => {
    await setupChannelsTable()

    const comboboxInput = screen.getByRole('combobox', { name: 'Filter by model...' })
    expect(comboboxInput).toBeDefined()
    expect(comboboxInput.getAttribute('placeholder')).toBe('Filter by model...')
  })

  it('populates model dropdown with enabled models and channel models', async () => {
    const user = userEvent.setup()
    await setupChannelsTable({
      enabledModels: ['gpt-4o', 'claude-3-5-sonnet'],
      items: [testChannel(1, 'openai-prod', 'qwen-max')],
    })

    const comboboxInput = screen.getByRole('combobox', { name: 'Filter by model...' })
    await user.click(comboboxInput)

    // Dropdown should be open and display the aggregated models:
    // 'claude-3-5-sonnet', 'gpt-4o' (from enabled models) and 'qwen-max' (from channel)
    await waitFor(() => {
      expect(screen.getByRole('option', { name: 'gpt-4o' })).toBeDefined()
      expect(screen.getByRole('option', { name: 'claude-3-5-sonnet' })).toBeDefined()
      expect(screen.getByRole('option', { name: 'qwen-max' })).toBeDefined()
    })
  })

  it('selects a model from the dropdown and updates the filter input', async () => {
    const user = userEvent.setup()
    const { getSpy } = await setupChannelsTable({
      enabledModels: ['gpt-4o', 'claude-3-5-sonnet'],
    })

    const comboboxInput = screen.getByRole('combobox', { name: 'Filter by model...' })
    await user.click(comboboxInput)

    const option = await screen.findByRole('option', { name: 'claude-3-5-sonnet' })
    await user.click(option)

    expect(comboboxInput).toHaveValue('claude-3-5-sonnet')

    // Debounced search will be triggered with the selected model
    await waitFor(
      () => {
        const searchCalls = getSpy.mock.calls.filter(([url]) => url === '/api/channel/search')
        const matched = searchCalls.some(([_, config]) => config?.params?.model === 'claude-3-5-sonnet')
        expect(matched).toBe(true)
      },
      { timeout: 2000 }
    )
  })

  it('clears model filter when toolbar reset is clicked', async () => {
    const user = userEvent.setup()
    await setupChannelsTable({
      initialUrl: '/channels/?model=gpt-4o',
    })

    const comboboxInput = screen.getByRole('combobox', { name: 'Filter by model...' })
    await waitFor(() => {
      expect(comboboxInput).toHaveValue('gpt-4o')
    })

    const resetButton = screen.getByRole('button', { name: /reset/i })
    await user.click(resetButton)

    await waitFor(() => {
      expect(comboboxInput).toHaveValue('')
    })
  })
})
