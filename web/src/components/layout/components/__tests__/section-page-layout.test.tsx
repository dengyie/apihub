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
import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { SectionPageLayout } from '../section-page-layout'

describe('console page landmarks', () => {
  it('exposes the page title as its main heading alongside actions and content', () => {
    render(
      <SectionPageLayout>
        <SectionPageLayout.Title>Channel management</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <button type='button'>Add channel</button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <p>Configured channels</p>
        </SectionPageLayout.Content>
      </SectionPageLayout>
    )
    const main = within(screen.getByRole('main'))
    expect(
      main.getByRole('heading', { level: 1, name: 'Channel management' })
    ).toBeVisible()
    expect(main.getByRole('button', { name: 'Add channel' })).toBeEnabled()
    expect(main.getByText('Configured channels')).toBeVisible()
  })
})
