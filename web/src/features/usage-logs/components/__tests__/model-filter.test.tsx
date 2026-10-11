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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import {
  cleanup,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider } from '../usage-logs-provider'

const pointerCaptureDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

function FilterFixture() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <UsageLogsProvider>
      <CommonLogsFilterBar table={table} />
    </UsageLogsProvider>
  )
}

type ModelsResponse = { success: boolean; data?: string[] }

async function renderFilter(modelsResponse: ModelsResponse) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/models') return { data: modelsResponse }
    if (url === '/api/user/self/groups') {
      return { data: { success: true, data: {} } }
    }
    return { data: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: FilterFixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: ['/usage-logs/common'],
    }),
  })
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  if (window.matchMedia('(max-width: 640px)').matches) {
    await userEvent.click(await screen.findByRole('button', { name: /^Filter/ }))
  }
  await screen.findByRole('combobox', { name: 'Model Name' })
  return router
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.setUser(null)
  if (pointerCaptureDescriptor) {
    Object.defineProperty(
      HTMLElement.prototype,
      'setPointerCapture',
      pointerCaptureDescriptor
    )
  } else Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
})

it('offers model choices and submits the selected model', async () => {
  const router = await renderFilter({
    success: true,
    data: ['gpt-5', 'claude-opus-4.8'],
  })
  const input = screen.getByRole('combobox', { name: 'Model Name' })
  await userEvent.click(input)
  await userEvent.click(await screen.findByRole('option', { name: 'gpt-5' }))
  expect(input).toHaveValue('gpt-5')
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      model: 'gpt-5',
      page: 1,
    })
  )
})

it.each([
  ['business failure', { success: false }],
  ['non-array payload', { success: true, data: undefined }],
])(
  'falls back to free-text input without any toast when the model list fails (%s)',
  async (_label, modelsResponse) => {
    const notify = vi.spyOn(toast, 'error').mockReturnValue('noop')
    const router = await renderFilter(
      modelsResponse as ModelsResponse
    )
    const input = screen.getByRole('combobox', { name: 'Model Name' })
    await userEvent.type(input, 'legacy-model-name')
    expect(input).toHaveValue('legacy-model-name')
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({
        model: 'legacy-model-name',
        page: 1,
      })
    )
    expect(notify).not.toHaveBeenCalled()
  }
)

it('falls back to free-text input without any toast when the model request rejects', async () => {
  const notify = vi.spyOn(toast, 'error').mockReturnValue('noop')
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/models') throw new Error('network down')
    if (url === '/api/user/self/groups') {
      return { data: { success: true, data: {} } }
    }
    return { data: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: FilterFixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: ['/usage-logs/common'] }),
  })
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('combobox', { name: 'Model Name' })
  const input = screen.getByRole('combobox', { name: 'Model Name' })
  await userEvent.type(input, 'legacy-model-name')
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      model: 'legacy-model-name',
      page: 1,
    })
  )
  expect(notify).not.toHaveBeenCalled()
})
