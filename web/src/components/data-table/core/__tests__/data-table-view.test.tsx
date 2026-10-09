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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'

import { DataTableView } from '../data-table-view'

function Fixture() {
  const table = useReactTable({
    data: [{ model: 'MangoApi', actions: '编辑' }],
    columns: [
      { accessorKey: 'model', header: '模型' },
      { accessorKey: 'actions', header: '操作' },
    ],
    getCoreRowModel: getCoreRowModel(),
  })

  return <DataTableView table={table} splitHeader />
}

it('pins each header cell when the table uses an internal scroll area', () => {
  render(<Fixture />)

  for (const header of screen.getAllByRole('columnheader')) {
    expect(header).toHaveClass('sticky', 'top-0', 'z-10')
    expect(header).toHaveClass('bg-(--table-header-bg)')
  }
})
