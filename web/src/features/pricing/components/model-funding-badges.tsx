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
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'

const fundingTags = new Map([
  ['free', 'Free'],
  ['免费', 'Free'],
  ['免費', 'Free'],
  ['sponsored', 'Sponsored'],
  ['赞助', 'Sponsored'],
  ['贊助', 'Sponsored'],
  ['opensource', 'Open source'],
  ['开源', 'Open source'],
  ['開源', 'Open source'],
  ['selfhosted', 'Self-hosted'],
  ['自建', 'Self-hosted'],
  ['自托管', 'Self-hosted'],
])

/** Funding comes only from explicit operator metadata, never a model name or price. */
export function ModelFundingBadges(props: { tags: string[] }) {
  const { t } = useTranslation()
  const labels = [
    ...new Set(
      props.tags.flatMap((tag) => {
        const label = fundingTags.get(
          tag.toLowerCase().replaceAll(/[\s_-]/g, '')
        )
        return label ? [label] : []
      })
    ),
  ]
  if (labels.length === 0) return null

  return (
    <div
      role='group'
      aria-label={t('Model funding')}
      className='flex flex-wrap gap-1.5'
    >
      {labels.map((label) => (
        <StatusBadge
          key={label}
          label={t(label)}
          type='badge'
          variant='neutral'
          copyable={false}
          className='border-border bg-muted/50 text-foreground h-5 rounded-full border px-2 text-[10px]'
        />
      ))}
    </div>
  )
}
