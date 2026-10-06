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
/**
 * Column definitions factory
 */
import type { ColumnDef } from '@tanstack/react-table'

import { useCommonLogsColumns } from '../components/columns/common-logs-columns'
import type { LogCategory } from '../types'

/**
 * Get column definitions based on log category
 */
export function useColumnsByCategory(
  _logCategory?: LogCategory,
  isAdmin = false,
  isRoot = false,
  showBillingSource = false
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
): ColumnDef<any>[] {
  return useCommonLogsColumns(isAdmin, isRoot, showBillingSource)
}
