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
import { useLayoutEffect } from 'react'

import { SearchProvider } from '@/context/search-provider'

import { AstryxAppShell } from './astryx-app-shell'

type AuthenticatedLayoutProps = {
  children?: React.ReactNode
}

export function AuthenticatedLayout(props: AuthenticatedLayoutProps) {
  // Scope the console reskin (`styles/console-skin.css`) and Astryx shell
  // to the signed-in routes only. A layout effect, not a passive one: the skin
  // has to be in place before the first console paint or the shell flashes the public
  // palette on the way in.
  useLayoutEffect(() => {
    document.body.dataset.designLayer = 'console'
    document.body.dataset.snowapiConsole = 'true'
    return () => {
      delete document.body.dataset.designLayer
      delete document.body.dataset.snowapiConsole
    }
  }, [])

  return (
    <SearchProvider>
      <AstryxAppShell>{props.children}</AstryxAppShell>
    </SearchProvider>
  )
}
