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
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { AstryxAppShell } from '../astryx-app-shell'

describe('AstryxAppShell and Navigation', () => {
  let queryClient: QueryClient

  beforeEach(() => {
    useSystemConfigStore
      .getState()
      .setConfig({ systemName: 'Gateway workspace' })
    useSystemConfigStore.getState().setLoading(false)
    queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
      },
    })
    queryClient.setQueryData(STATUS_QUERY_KEY, {})
  })

  afterEach(() => {
    cleanup()
    queryClient.clear()
    useSystemConfigStore.getState().setConfig({ systemName: 'New API' })
  })

  function renderWithRouter(
    initialEntry: string,
    Page = () => <div data-testid='dashboard-content'>Dashboard</div>
  ) {
    const rootRoute = createRootRoute({
      component: () => (
        <ThemeProvider>
          <DirectionProvider>
            <AstryxAppShell>
              <Outlet />
            </AstryxAppShell>
          </DirectionProvider>
        </ThemeProvider>
      ),
    })

    const dashboardRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/dashboard',
      component: Page,
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
    const logoLink = screen.getAllByRole('link', {
      name: 'Gateway workspace',
    })[0]
    expect(logoLink).toHaveAttribute('href', '/dashboard')
  })

  it('keeps account and appearance controls reachable from the sidebar', async () => {
    renderWithRouter('/dashboard')
    expect(await screen.findByTestId('dashboard-content')).toBeVisible()
    expect(
      screen.getAllByRole('button', { name: 'Toggle theme' }).length
    ).toBeGreaterThan(0)
    expect(
      screen.getAllByRole('button', { name: 'Change language' }).length
    ).toBeGreaterThan(0)
    expect(
      screen.getAllByRole('button', { name: 'Sign out' })[0]
    ).toHaveAttribute('aria-label', 'Sign out')
  })

  it('shows the page skeleton only until an observed query has initial data', async () => {
    let finish = (_value: string) => {}
    const response = new Promise<string>((resolve) => {
      finish = resolve
    })
    function Page() {
      const query = useQuery({
        queryKey: ['first-page'],
        queryFn: () => response,
      })
      return <p>{query.data ?? 'Waiting for page data'}</p>
    }
    renderWithRouter('/dashboard', Page)
    expect(
      await screen.findByRole('status', { name: 'Loading page' })
    ).toBeVisible()
    await act(async () => {
      finish('Page data ready')
    })
    expect(await screen.findByText('Page data ready')).toBeVisible()
    await waitFor(() =>
      expect(
        screen.queryByRole('status', { name: 'Loading page' })
      ).not.toBeInTheDocument()
    )
  })

  it('keeps existing content visible during an observed background refetch', async () => {
    let finish = (_value: string) => {}
    const response = new Promise<string>((resolve) => {
      finish = resolve
    })
    queryClient.setQueryData(['cached-page'], 'Previously loaded data')
    function Page() {
      const query = useQuery({
        queryKey: ['cached-page'],
        queryFn: () => response,
      })
      return <p>{query.data}</p>
    }
    renderWithRouter('/dashboard', Page)
    expect(await screen.findByText('Previously loaded data')).toBeVisible()
    expect(
      screen.queryByRole('status', { name: 'Loading page' })
    ).not.toBeInTheDocument()
    await act(async () => {
      finish('Fresh page data')
    })
    expect(await screen.findByText('Fresh page data')).toBeVisible()
  })

  it('does not cover the page for an inactive prefetch without data', async () => {
    let finish = (_value: string) => {}
    const response = new Promise<string>((resolve) => {
      finish = resolve
    })
    const prefetch = queryClient.prefetchQuery({
      queryKey: ['prefetch'],
      queryFn: () => response,
    })
    renderWithRouter('/dashboard')
    expect(await screen.findByTestId('dashboard-content')).toBeVisible()
    expect(
      screen.queryByRole('status', { name: 'Loading page' })
    ).not.toBeInTheDocument()
    await act(async () => {
      finish('Prefetched data')
      await prefetch
    })
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
        <ThemeProvider>
          <DirectionProvider>
            <AstryxAppShell>
              <Outlet />
            </AstryxAppShell>
          </DirectionProvider>
        </ThemeProvider>
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
