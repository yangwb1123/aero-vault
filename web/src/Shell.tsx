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
import { ActivityPage } from './pages/ActivityPage'
import { FilesPage } from './pages/FilesPage'
import { AccessPage } from './pages/access/AccessPage'
import { ChatPage } from './pages/ChatPage'
import { BucketsPage } from './pages/buckets/BucketsPage'
import { DepartmentsPage } from './pages/departments/DepartmentsPage'
import { LineagePage } from './pages/LineagePage'
import { OverviewPage } from './pages/OverviewPage'
import { ObjectPage } from './pages/object/ObjectPage'
import { OperationsPage } from './pages/operations/OperationsPage'
import { SearchPage } from './pages/SearchPage'
import { ServicesPage } from './pages/ServicesPage'
import { objectRoute, parseObjectRoute, useHashRoute } from './router'

const menus: NavNode[] = [
  { key: 'overview', title: '空间概览', icon: 'home' },
  { key: 'files', title: '文件', icon: 'folder' },
  { key: 'activity', title: '对象动态', icon: 'clock' },
  { key: 'buckets', title: '存储桶', icon: 'archive' },
  { key: 'access', title: '访问与发布', icon: 'lock' },
  { key: 'departments', title: '部门与成员', icon: 'users' },
  { key: 'search', title: '知识检索', icon: 'search' },
  { key: 'chat', title: '知识 Chat', icon: 'link' },
  { key: 'lineage', title: '对象血缘', icon: 'clock' },
  { key: 'operations', title: '运行运维', icon: 'settings' },
  { key: 'services', title: '平台服务', icon: 'grid' },
]
const routeKeys = new Set(menus.map((item) => item.key))

function PageHost({
  route,
  client,
  config,
  navigate,
}: {
  route: string
  client: VaultClient
  config: WebConfig
  navigate(route: string): void
}): React.ReactElement {
  const object = parseObjectRoute(route)
  if (object) return <ObjectPage key={`${object.deleted}:${object.key}`} client={client} objectKey={object.key} deleted={object.deleted} onBack={() => navigate('files')} onRestored={() => navigate(objectRoute(object.key))} />
  if (route === 'files') return <FilesPage client={client} onOpenObject={(key, deleted) => navigate(objectRoute(key, deleted))} />
  if (route === 'activity') return <ActivityPage client={client} onOpenObject={(key, deleted) => navigate(objectRoute(key, deleted))} />
  if (route === 'buckets') return <BucketsPage client={client} />
  if (route === 'access') return <AccessPage client={client} />
  if (route === 'departments') return <DepartmentsPage client={client} />
  if (route === 'search') return <SearchPage client={client} />
  if (route === 'chat') return <ChatPage client={client} />
  if (route === 'lineage') return <LineagePage client={client} />
  if (route === 'operations') return <OperationsPage client={client} />
  if (route === 'services') return <ServicesPage config={config} />
  return <OverviewPage client={client} />
}

export function Shell({ config }: { config: WebConfig }): React.ReactElement {
  const auth = useAuth()
  const { skin, setMode, setSkin } = useSkin()
  const [route, navigate] = useHashRoute('overview')
  const object = parseObjectRoute(route)
  const pageRoute = routeKeys.has(route) || object ? route : 'overview'
  const activeRoute = object ? 'files' : pageRoute
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
      <PageHost route={pageRoute} client={client} config={config} navigate={navigate} />
    </IrisAdminLayout>
  )
}
