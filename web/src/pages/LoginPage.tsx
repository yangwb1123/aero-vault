import { IrisAlert, IrisButton, IrisCard } from '@iris-ui-kit/react'
import { useAuth } from '../auth/AuthProvider'
import type { WebConfig } from '../config'

export function LoginPage({ config }: { config: WebConfig }): React.ReactElement {
  const auth = useAuth()
  return (
    <main className="login-shell">
      <IrisCard
        className="login-card"
        variant="elevated"
        header={<div className="login-brand">Aero Vault</div>}
      >
        <h1>文件与知识空间</h1>
        <p>
          使用 Snaplink 完成统一登录。浏览器只在当前页面内存中持有短期令牌，刷新后需要重新登录。
        </p>
        <IrisButton size="lg" onClick={auth.login}>
          使用 Snaplink 登录
        </IrisButton>
        {config.allowAnonymous ? (
          <>
            <IrisAlert tone="warning" title="本地开发模式">
              匿名入口不会绕过服务端鉴权；仅适用于未启用鉴权的本地 Aero Vault。
            </IrisAlert>
            <IrisButton variant="ghost" onClick={auth.continueAnonymous}>
              继续本地开发
            </IrisButton>
          </>
        ) : null}
      </IrisCard>
    </main>
  )
}
