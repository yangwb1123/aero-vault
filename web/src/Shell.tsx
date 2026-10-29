import * as React from 'react'
import {
  IrisAdminLayout,
  IrisAvatar,
  IrisBadge,
  IrisButton,
  IrisIcon,
  useSkin,
} from '@iris-ui-kit/react'
import { VaultClient } from './api/vault'
import { useAuth } from './auth/AuthProvider'
import type { WebConfig } from './config'
import { PageHeader } from './components/Page'
import { PageBoundary } from './components/PageBoundary'
import { useSessionNavigation } from './hooks/useSessionNavigation'
import { isManagementRoute, navigationMenus, routeKeys } from './navigation'
import { ActivityPage } from './pages/ActivityPage'
import { AgentPage } from './pages/AgentPage'
import { FilesPage } from './pages/FilesPage'
import { fileLocationRoute, parseFileLocationRoute } from './pages/files/fileLocation'
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
import { objectRoute, parseObjectRoute, routeFromHash, useHashRoute, type ObjectRoute } from './router'

type SessionNavigation = ReturnType<typeof useSessionNavigation>
type NavigateRoute = ReturnType<typeof useHashRoute>[1]
const clientPages: Record<string, React.ComponentType<{ client: VaultClient }>> = {
  buckets: BucketsPage, access: AccessPage, departments: DepartmentsPage,
  search: SearchPage, chat: ChatPage, agent: AgentPage, lineage: LineagePage,
  operations: OperationsPage,
}

function PageHost({ route, client, config, navigate, navigation }: {
  route: string
  client: VaultClient
  config: WebConfig
  navigate: NavigateRoute
  navigation: SessionNavigation
}): React.ReactElement {
  if (isManagementRoute(route) && !navigation.management[route]) {
    return <UnavailableManagement route={route} navigation={navigation} navigate={navigate} />
  }
  const object = parseObjectRoute(route)
  if (object) return <RoutedObjectPage key={route} route={route} object={object} client={client} navigate={navigate} />
  const location = parseFileLocationRoute(route)
  if (location) return <FilesPage client={client} location={location}
    onLocationChange={(next) => navigate(fileLocationRoute(next))}
    onOpenObject={(key, deleted) => navigate(objectRoute(key, deleted, location))} />
  if (route === 'activity') return <ActivityPage client={client} onOpenObject={(key, deleted) => navigate(objectRoute(key, deleted))} />
  if (route === 'services') return <ServicesPage config={config} />
  const Page = clientPages[route] ?? OverviewPage
  return <Page client={client} />
}

function RoutedObjectPage({ route, object, client, navigate }: {
  route: string; object: ObjectRoute; client: VaultClient; navigate: NavigateRoute
}): React.ReactElement {
  const fence = React.useMemo(() => ({ active: true }), [client, route])
  React.useLayoutEffect(() => {
    fence.active = true
    return () => { fence.active = false }
  }, [fence])
  return <ObjectPage client={client} objectKey={object.key} deleted={object.deleted}
    onBack={() => navigate(object.returnTo ? fileLocationRoute(object.returnTo) : 'files')}
    onRestored={() => {
      if (fence.active && routeFromHash('') === route) {
        navigate(objectRoute(object.key, false, object.returnTo), { replace: true })
      }
    }} />
}

function UnavailableManagement({ route, navigation, navigate }: {
  route: string
  navigation: SessionNavigation
  navigate(route: string): void
}): React.ReactElement {
  const pending = navigation.status === 'loading' || (route === 'operations' && navigation.operatorLoading)
  const description = pending
    ? '正在通过服务端确认会话与管理入口资格，确认前不会加载管理页面。'
    : '当前会话尚未确认此管理入口的访问资格；会话读取或入口检查失败时也不会加载。登录、文件归属或写入权限不代表管理权限，具体资源仍由服务端授权。'
  return (
    <section>
      <PageHeader title="管理入口暂不可用" description={description} />
      {route === 'operations' ? <p className="muted">跨租户运维需要服务端 operator admin 检查通过；租户管理资格不代表跨租户权限。</p> : null}
      <IrisButton variant="outline" onClick={() => navigate('files')}>返回文件</IrisButton>
      <IrisButton variant="ghost" onClick={navigation.reload}>重新检查会话</IrisButton>
    </section>
  )
}

function ShellToolbar({ navigation }: { navigation: SessionNavigation }): React.ReactElement {
  const auth = useAuth()
  const { skin, setMode, setSkin } = useSkin()
  const dark = skin.type === 'dark'
  const authenticated = navigation.session?.authenticated
  const label = navigation.session ? authenticated ? '已认证' : '匿名会话' : '会话未确认'
  return (
    <div className="shell-toolbar">
      <IrisBadge tone={authenticated ? 'success' : 'warning'}>{label}</IrisBadge>
      <IrisButton
        variant="outline" size="sm" aria-label="切换主题"
        onClick={() => { setMode('fixed'); setSkin(dark ? 'light' : 'dark') }}
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
  )
}

export function Shell({ config }: { config: WebConfig }): React.ReactElement {
  const auth = useAuth()
  const [route, navigate] = useHashRoute('overview')
  const object = parseObjectRoute(route)
  const files = parseFileLocationRoute(route)
  const pageRoute = routeKeys.has(route) || object || files ? route : 'overview'
  const activeRoute = object || files ? 'files' : pageRoute
  const token = auth.session?.accessToken ?? ''
  const client = React.useMemo(
    () => new VaultClient(config.apiBase, () => token),
    [config.apiBase, token],
  )
  const navigation = useSessionNavigation(client)
  const { buckets, departments, operations } = navigation.management
  const menus = React.useMemo(() => navigationMenus({ buckets, departments, operations }), [buckets, departments, operations])

  return (
    <IrisAdminLayout
      menus={menus}
      activeKey={activeRoute}
      onActiveKeyChange={navigate}
      showTabs={false}
      appTitle="Aero Vault"
      toolbar={<ShellToolbar navigation={navigation} />}
      footer={<div className="shell-footer">文件由 Aero Vault 管理 · 身份由 Snaplink 验证 · UI 由 Iris UI 构建</div>}
    >
      <p className="muted">服务端租户：{navigation.session?.tenant_id ?? '未确认'} · 文件浏览桶：default（固定）</p>
      <PageBoundary resetKey={pageRoute}>
        <PageHost route={pageRoute} client={client} config={config} navigate={navigate} navigation={navigation} />
      </PageBoundary>
    </IrisAdminLayout>
  )
}
