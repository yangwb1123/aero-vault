export interface WebConfig {
  apiBase: string
  oidcLoginPath: string
  oidcLogoutPath: string
  allowAnonymous: boolean
  accountConsoleUrl?: string
  notificationConsoleUrl?: string
  auditConsoleUrl?: string
}

const trimSlash = (value: string): string => value.replace(/\/+$/, '')

function firstValue(...values: Array<string | undefined>): string | undefined {
  return values.find((value) => Boolean(value?.trim()))?.trim()
}

function optionalUrl(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized ? trimSlash(normalized) : undefined
}

function booleanValue(runtime: boolean | undefined, env: string | undefined): boolean {
  if (runtime !== undefined) return runtime
  return env?.toLowerCase() !== 'false'
}

export function loadWebConfig(): WebConfig {
  const runtime = window.__AERO_VAULT_WEB_CONFIG__ ?? {}
  return {
    apiBase: trimSlash(firstValue(runtime.apiBase, import.meta.env.VITE_AERO_VAULT_API_BASE, '/v1')!),
    oidcLoginPath: firstValue(
      runtime.oidcLoginPath,
      import.meta.env.VITE_AERO_VAULT_OIDC_LOGIN_PATH,
      '/auth/oidc/login',
    )!,
    oidcLogoutPath: firstValue(
      runtime.oidcLogoutPath,
      import.meta.env.VITE_AERO_VAULT_OIDC_LOGOUT_PATH,
      '/auth/oidc/logout',
    )!,
    allowAnonymous: booleanValue(
      runtime.allowAnonymous,
      import.meta.env.VITE_AERO_VAULT_ALLOW_ANONYMOUS,
    ),
    accountConsoleUrl: optionalUrl(
      firstValue(runtime.accountConsoleUrl, import.meta.env.VITE_AERO_ID_CONSOLE_URL),
    ),
    notificationConsoleUrl: optionalUrl(
      firstValue(runtime.notificationConsoleUrl, import.meta.env.VITE_AERO_IM_CONSOLE_URL),
    ),
    auditConsoleUrl: optionalUrl(
      firstValue(runtime.auditConsoleUrl, import.meta.env.VITE_AUDIT_CONSOLE_URL),
    ),
  }
}
