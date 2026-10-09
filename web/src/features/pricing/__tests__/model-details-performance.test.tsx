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
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ModelDetailsPerformance } from '../components/model-details-performance'
import type { PricingModel } from '../types'

// Canvas rendering is a browser boundary; these tests exercise query states.
vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: vi.fn() },
}))

const model: PricingModel = {
  id: 1,
  model_name: 'example-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
}
const emptyResponse = {
  data: {
    success: true,
    data: { model_name: model.model_name, groups: [], series: [] },
  },
}
let client: QueryClient

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
})
afterEach(() => {
  client.clear()
  vi.restoreAllMocks()
})

function renderPerformance() {
  return render(
    <QueryClientProvider client={client}>
      <ModelDetailsPerformance model={model} />
    </QueryClientProvider>
  )
}

describe('model performance availability', () => {
  it('keeps cached details visibly stale after refresh failure and replaces them after retry', async () => {
    const data = {
      model_name: model.model_name,
      groups: [],
      series: [],
      summary: { success_rate: 99.5, avg_latency_ms: 1200, avg_tps: 30 },
    }
    client.setQueryData(['perf-metrics', model.model_name, 24], {
      success: true,
      data,
    })
    vi.spyOn(api, 'get')
      .mockRejectedValueOnce(new Error('Offline'))
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { ...data, summary: { ...data.summary, success_rate: 95 } },
        },
      })
    renderPerformance()
    await act(async () => {
      await client.refetchQueries({
        queryKey: ['perf-metrics', model.model_name, 24],
      })
    })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Refresh failed. Showing the last successful snapshot.'
    )
    expect(screen.getByText('99.50%')).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('95.00%')).toBeVisible()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('keeps loading distinct from a successful response with no measurements', async () => {
    let complete!: (response: typeof emptyResponse) => void
    vi.spyOn(api, 'get').mockReturnValueOnce(
      new Promise((resolve) => {
        complete = resolve
      })
    )
    renderPerformance()
    expect(screen.getByRole('status')).toHaveTextContent('Loading...')
    expect(
      screen.queryByText(
        'Performance data is not yet available for this model.'
      )
    ).not.toBeInTheDocument()
    await act(async () => {
      complete(emptyResponse)
    })
    expect(
      await screen.findByText(
        'Performance data is not yet available for this model.'
      )
    ).toBeVisible()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('offers retry on request failure and shows the successful empty result after retry', async () => {
    vi.spyOn(api, 'get')
      .mockRejectedValueOnce(new Error('Offline'))
      .mockResolvedValueOnce(emptyResponse)
    renderPerformance()
    expect(
      await screen.findByText('Failed to load performance data')
    ).toBeVisible()
    expect(
      screen.queryByText(
        'Performance data is not yet available for this model.'
      )
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByText(
        'Performance data is not yet available for this model.'
      )
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Retry' })
    ).not.toBeInTheDocument()
  })

  it('treats an API business failure as an error rather than an empty result', async () => {
    vi.spyOn(api, 'get').mockResolvedValueOnce({
      data: { success: false, message: 'Metrics unavailable' },
    })
    renderPerformance()
    expect(
      await screen.findByText('Failed to load performance data')
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled()
  })
})
