import { IrisProvider } from '@iris-ui-kit/react'
import { localeZhPlugin } from '@iris-ui-kit/plugin-locale-zh/core'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { loadWebConfig, type WebConfig } from './config'
import { LoginPage } from './pages/LoginPage'
import { Shell } from './Shell'
import { vaultSkin } from './skin'

function AuthGate({ config }: { config: WebConfig }): React.ReactElement {
  const auth = useAuth()
  return auth.status === 'authenticated' ? <Shell config={config} /> : <LoginPage config={config} />
}

export function App(): React.ReactElement {
  const config = loadWebConfig()
  return (
    <IrisProvider skin={vaultSkin} plugins={[localeZhPlugin]} locale="zh-CN">
      <AuthProvider config={config}>
        <AuthGate config={config} />
      </AuthProvider>
    </IrisProvider>
  )
}
