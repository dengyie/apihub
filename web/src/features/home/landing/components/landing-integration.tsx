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
import { Terminal } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useStatus } from '@/hooks/use-status'

import { resolveApiBaseUrl } from '../lib/api-base-url'

export function LandingIntegration() {
  const { t } = useTranslation()
  const [language, setLanguage] = useState('python')
  const { status } = useStatus()
  const baseUrl = resolveApiBaseUrl(
    status?.server_address,
    window.location.origin
  )
  const examples: Record<string, string> = {
    python: `import os\nfrom openai import OpenAI\n\nclient = OpenAI(\n    api_key=os.environ["API_KEY"],\n    base_url="${baseUrl}",\n)\n\nresponse = client.chat.completions.create(\n    model=os.environ["MODEL"],\n    messages=[{"role": "user", "content": "Hello!"}],\n)\nprint(response.choices[0].message.content)`,
    javascript: `import OpenAI from "openai";\n\nconst client = new OpenAI({\n  apiKey: process.env.API_KEY,\n  baseURL: "${baseUrl}",\n});\n\nconst response = await client.chat.completions.create({\n  model: process.env.MODEL,\n  messages: [{ role: "user", content: "Hello!" }],\n});\nconsole.log(response.choices[0].message.content);`,
    curl: [
      `curl "${baseUrl}/chat/completions" \\`,
      '  -H "Authorization: Bearer $API_KEY" \\',
      '  -H "Content-Type: application/json" \\',
      '  --data @- <<EOF',
      '{',
      '  "model": "$MODEL",',
      '  "messages": [{"role": "user", "content": "Hello!"}]',
      '}',
      'EOF',
    ].join('\n'),
  }
  const code = examples[language] ?? examples.python

  return (
    <section
      className='landing-integration landing-section'
      aria-labelledby='integration-title'
    >
      <div className='landing-integration-copy'>
        <p className='landing-eyebrow'>{t('A familiar interface')}</p>
        <h2 id='integration-title'>
          {t('A small change.')}
          <em>{t('A wider world of models.')}</em>
        </h2>
        <p>
          {t(
            'Use the OpenAI SDK you know. Set your endpoint, add a key from the console, and choose a model from the catalog.'
          )}
        </p>
        <div className='landing-endpoint'>
          <div>
            <span>{t('API base URL')}</span>
            <code>{baseUrl}</code>
          </div>
          <CopyButton value={baseUrl} tooltip={t('Copy API base URL')} />
        </div>
        <p className='landing-example-note'>
          {t(
            'Set API_KEY and MODEL in your environment. This example runs on your server so your key stays private.'
          )}
        </p>
      </div>
      <div className='landing-code-window'>
        <Tabs value={language} onValueChange={setLanguage}>
          <div className='landing-code-bar'>
            <Terminal size={16} aria-hidden='true' />
            <TabsList variant='line' aria-label={t('Example language')}>
              <TabsTrigger value='python'>Python</TabsTrigger>
              <TabsTrigger value='javascript'>JavaScript</TabsTrigger>
              <TabsTrigger value='curl'>cURL</TabsTrigger>
            </TabsList>
            <CopyButton value={code} tooltip={t('Copy example')} />
          </div>
          <pre tabIndex={0} aria-label={t('Integration example')}>
            <code>{code}</code>
          </pre>
        </Tabs>
        <div className='landing-code-foot'>
          <span className='landing-code-dot' />
          {t('OpenAI-compatible API')}
          <span>/v1/chat/completions</span>
        </div>
      </div>
    </section>
  )
}
