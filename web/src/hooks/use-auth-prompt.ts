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
import { useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'

import type { TopNavLink } from '@/components/layout/types'

const AUTH_PROMPT_SECONDS = 5

type AuthPromptTarget = {
  title: string
  href: string
}

export type AuthPrompt = {
  target: AuthPromptTarget | null
  secondsLeft: number
  interceptLinkClick: (
    event: React.MouseEvent<HTMLAnchorElement>,
    link: TopNavLink
  ) => boolean
  close: () => void
  goToSignIn: () => void
}

/**
 * The single place a nav link click is arbitrated.
 *
 * A link can be inert (`disabled`) or gated behind sign-in (`requiresAuth`,
 * produced for the pricing and rankings modules when the operator enables
 * `requireAuth` in 运营设置 → 导航配置). Both outcomes must stop the browser
 * navigation; the second additionally runs a countdown before redirecting.
 *
 * Every navigation surface routes its clicks through
 * {@link AuthPrompt.interceptLinkClick} so the two rules cannot drift apart —
 * the page still renders correctly without the router guard, it just
 * redirects on its own.
 */
export function useAuthPrompt(): AuthPrompt {
  const navigate = useNavigate()
  const [target, setTarget] = useState<AuthPromptTarget | null>(null)
  const [secondsLeft, setSecondsLeft] = useState(AUTH_PROMPT_SECONDS)

  useEffect(() => {
    if (!target) return

    const intervalId = window.setInterval(() => {
      setSecondsLeft((seconds) => Math.max(seconds - 1, 0))
    }, 1000)
    const timeoutId = window.setTimeout(() => {
      const redirect = target.href
      setTarget(null)
      void navigate({ to: '/sign-in', search: { redirect } })
    }, AUTH_PROMPT_SECONDS * 1000)

    return () => {
      window.clearInterval(intervalId)
      window.clearTimeout(timeoutId)
    }
  }, [target, navigate])

  const close = useCallback(() => {
    setTarget(null)
    setSecondsLeft(AUTH_PROMPT_SECONDS)
  }, [])

  const goToSignIn = useCallback(() => {
    const redirect = target?.href || '/'
    setTarget(null)
    void navigate({ to: '/sign-in', search: { redirect } })
  }, [target?.href, navigate])

  const interceptLinkClick = useCallback(
    (event: React.MouseEvent<HTMLAnchorElement>, link: TopNavLink) => {
      if (link.disabled) {
        event.preventDefault()
        return true
      }

      if (!link.requiresAuth) return false

      event.preventDefault()
      setSecondsLeft(AUTH_PROMPT_SECONDS)
      setTarget({ title: link.title, href: link.href })
      return true
    },
    []
  )

  return { target, secondsLeft, interceptLinkClick, close, goToSignIn }
}
