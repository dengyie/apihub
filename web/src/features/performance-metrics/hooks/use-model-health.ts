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
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'

import { requireServerSuccess } from '@/lib/server-error-message'

import { getPerfMetricsSummary } from '../api'

export function useModelHealth() {
  const query = useQuery({
    queryKey: ['perf-metrics-summary', 24],
    queryFn: async () => requireServerSuccess(await getPerfMetricsSummary(24)),
    staleTime: 60_000,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    retry: false,
  })
  const data = query.data?.data
  const models = useMemo(
    () =>
      new Map(
        data?.models.map((model) => [
          model.model_name,
          {
            ...model,
            window_start: data.window_start,
            window_end: data.window_end,
          },
        ])
      ),
    [data]
  )
  return {
    models,
    summary: data?.summary,
    isLoading: query.isLoading,
    updatedAt: query.dataUpdatedAt,
    error: query.error,
    refetch: query.refetch,
  }
}
