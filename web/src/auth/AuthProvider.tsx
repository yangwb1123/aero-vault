import * as React from 'react'
import type { WebConfig } from '../config'
import { consumeOIDCCallback, type BrowserSession } from './callback'

type AuthStatus = 'anonymous' | 'authenticated'

interface AuthContextValue {
  status: AuthStatus
  session?: BrowserSession
  login(): void
  logout(): void
  continueAnonymous(): void
}

const AuthContext = React.createContext<AuthContextValue | null>(null)

export function AuthProvider({
  config,
  children,
}: {
  config: WebConfig
  children: React.ReactNode
}): React.ReactElement {
  const initial = React.useMemo(() => consumeOIDCCallback(window.location.href), [])
  const [session, setSession] = React.useState(initial.session)
  const [anonymousAccepted, setAnonymousAccepted] = React.useState(false)

  React.useEffect(() => {
    if (initial.session) window.history.replaceState(null, '', initial.cleanUrl)
  }, [initial])

  React.useEffect(() => {
    if (!session?.expiresAt) return
    const delay = Math.max(0, session.expiresAt - Date.now())
    const timer = window.setTimeout(() => setSession(undefined), delay)
    return () => window.clearTimeout(timer)
  }, [session])

  const value = React.useMemo<AuthContextValue>(
    () => ({
      status: session || anonymousAccepted ? 'authenticated' : 'anonymous',
      session,
      login: () => window.location.assign(config.oidcLoginPath),
      logout: () => {
        setSession(undefined)
        setAnonymousAccepted(false)
        window.location.assign(config.oidcLogoutPath)
      },
      continueAnonymous: () => setAnonymousAccepted(true),
    }),
    [anonymousAccepted, config.oidcLoginPath, config.oidcLogoutPath, session],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const value = React.useContext(AuthContext)
  if (!value) throw new Error('useAuth must be used inside AuthProvider')
  return value
}
