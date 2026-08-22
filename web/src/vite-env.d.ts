/// <reference types="vite/client" />

interface AeroVaultWebRuntimeConfig {
  apiBase?: string
  oidcLoginPath?: string
  oidcLogoutPath?: string
  allowAnonymous?: boolean
  accountConsoleUrl?: string
  notificationConsoleUrl?: string
  auditConsoleUrl?: string
}

interface Window {
  __AERO_VAULT_WEB_CONFIG__?: AeroVaultWebRuntimeConfig
}
