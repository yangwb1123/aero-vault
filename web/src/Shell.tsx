import * as React from 'react'
import {
  IrisAdminLayout,
  IrisAvatar,
  IrisBadge,
  IrisButton,
  IrisIcon,
  useSkin,
  type NavNode,
} from '@iris-ui-kit/react'
import { VaultClient } from './api/vault'
import { useAuth } from './auth/AuthProvider'
import type { WebConfig } from './config'
import { FilesPage } from './pages/FilesPage'
import { OverviewPage } from './pages/OverviewPage'
import { ServicesPage } from './pages/ServicesPage'
import { useHashRoute } from './router'

const menus: NavNode[] = [
  { key: 'overview', title: '空间概览', icon: 'home' },
  { key: 'files', title: '文件', icon: 'folder' },
  { key: 'services', title: '平台服务', icon: 'grid' },
]
const routeKeys = new Set(menus.map((item) => item.key))

function PageHost({
  route,
  client,
  config,
}: {
  route: string
  client: VaultClient
  config: WebConfig
}): React.ReactElement {
  if (route === 'files') return <FilesPage client={client} />
  if (route === 'services') return <ServicesPage config={config} />
  return <OverviewPage client={client} />
}

export function Shell({ config }: { config: WebConfig }): React.ReactElement {
  const auth = useAuth()
  const { skin, setMode, setSkin } = useSkin()
  const [route, navigate] = useHashRoute('overview')
  const activeRoute = routeKeys.has(route) ? route : 'overview'
  const token = auth.session?.accessToken ?? ''
  const client = React.useMemo(
    () => new VaultClient(config.apiBase, () => token),
    [config.apiBase, token],
  )
  const dark = skin.type === 'dark'

  return (
    <IrisAdminLayout
      menus={menus}
      activeKey={activeRoute}
      onActiveKeyChange={navigate}
      showTabs={false}
      appTitle="Aero Vault"
      toolbar={
        <div className="shell-toolbar">
          <IrisBadge tone={auth.session ? 'success' : 'warning'}>
            {auth.session ? 'Snaplink 已认证' : '匿名开发'}
          </IrisBadge>
          <IrisButton
            variant="outline"
            size="sm"
            aria-label="切换主题"
            onClick={() => {
              setMode('fixed')
              setSkin(dark ? 'light' : 'dark')
            }}
          >
            <IrisIcon name={dark ? 'sun' : 'moon'} size={16} />
          </IrisButton>
          <IrisAvatar name="Aero User" size={32} />
          {auth.session ? (
            <IrisButton variant="ghost" size="sm" onClick={auth.logout}>退出</IrisButton>
          ) : (
            <IrisButton variant="ghost" size="sm" onClick={auth.login}>Snaplink 登录</IrisButton>
          )}
        </div>
      }
      footer={<div className="shell-footer">文件由 Aero Vault 管理 · 身份由 Snaplink 验证 · UI 由 Iris UI 构建</div>}
    >
      <PageHost route={activeRoute} client={client} config={config} />
    </IrisAdminLayout>
  )
}
