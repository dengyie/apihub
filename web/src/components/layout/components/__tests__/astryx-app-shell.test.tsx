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
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { DirectionProvider } from '@/context/direction-provider'

import { AstryxAppShell } from '../astryx-app-shell'

describe('AstryxAppShell and Navigation', () => {
  let queryClient: QueryClient

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
      },
    })
  })

  afterEach(() => {
    cleanup()
    queryClient.clear()
  })

  function renderWithRouter(initialEntry: string) {
    const rootRoute = createRootRoute({
      component: () => (
        <DirectionProvider>
          <AstryxAppShell>
            <Outlet />
          </AstryxAppShell>
        </DirectionProvider>
      ),
    })

    const dashboardRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/dashboard',
      component: () => <div data-testid='dashboard-content'>Dashboard</div>,
    })

    const systemSettingsRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/system-settings/general',
      component: () => (
        <div data-testid='settings-content'>System Settings</div>
      ),
    })

    const router = createRouter({
      routeTree: rootRoute.addChildren([dashboardRoute, systemSettingsRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    })

    return render(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
  }

  it('renders navigation links mapped to TanStack Router without falling back to root', async () => {
    renderWithRouter('/dashboard')

    // Expect dashboard content to render
    expect(await screen.findByTestId('dashboard-content')).toBeInTheDocument()

    // AstryxNavigation should contain links with valid href
    const logoLink = screen.getAllByRole('link', { name: /SnowAPI/i })[0]
    expect(logoLink).toHaveAttribute('href', '/dashboard')
  })

  it('renders subview topContent with parent link and ArrowLeft icon', async () => {
    renderWithRouter('/system-settings/general')

    expect(await screen.findByTestId('settings-content')).toBeInTheDocument()

    // In system settings, a return item to Dashboard is rendered in topContent
    const returnLinks = screen.getAllByRole('link').filter((link) => {
      return (
        link.getAttribute('href') === '/dashboard/overview' &&
        link.textContent?.includes('Back to Dashboard')
      )
    })
    expect(returnLinks.length).toBeGreaterThan(0)

    // The return item should render an icon (ArrowLeft)
    const svgIcon = returnLinks[0].querySelector('svg')
    expect(svgIcon).toBeInTheDocument()
  })

  it('renders authenticated error route cleanly without crashing on missing layout providers', async () => {
    const rootRoute = createRootRoute({
      component: () => (
        <DirectionProvider>
          <AstryxAppShell>
            <Outlet />
          </AstryxAppShell>
        </DirectionProvider>
      ),
    })

    const errorRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/errors/$error',
      component: () => (
        <div className='flex-1 p-4 [&>div]:h-full'>
          <div data-testid='error-view'>Error View</div>
        </div>
      ),
    })

    const router = createRouter({
      routeTree: rootRoute.addChildren([errorRoute]),
      history: createMemoryHistory({ initialEntries: ['/errors/not-found'] }),
    })

    render(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )

    expect(await screen.findByTestId('error-view')).toBeInTheDocument()
  })
})
