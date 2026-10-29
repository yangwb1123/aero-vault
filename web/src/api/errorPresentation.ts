import { VaultApiError } from './transport'

type ReadFailureGuidance = { title: string; guidance: string }

const genericFailure: ReadFailureGuidance = {
  title: '读取失败',
  guidance: '请检查网络连接和请求信息，确认后可手动重试读取；如持续失败，请联系管理员。',
}
const serviceUnavailable: ReadFailureGuidance = {
  title: '服务暂不可用',
  guidance: '请稍后手动重试读取；如持续失败，请提供请求 ID（如有）联系管理员。',
}
const conflict: ReadFailureGuidance = {
  title: '资源状态冲突',
  guidance: '请先核对最新资源状态和版本，再决定下一步；不会自动重放操作。',
}
const httpFailures: Record<number, ReadFailureGuidance> = {
  401: {
    title: '需要身份认证',
    guidance: '请确认已通过当前页面的登录入口完成认证，再手动重试读取；页面不会自动更新凭据。',
  },
  403: {
    title: '没有访问权限',
    guidance: '请核对当前账号和资源范围，或联系管理员确认权限；重复请求不会获得授权。',
  },
  404: {
    title: '资源不存在或不可用',
    guidance: '请核对路径和所选版本，或返回列表查看；资源也可能已删除或不再可访问。',
  },
  409: conflict,
  412: conflict,
  413: {
    title: '请求大小超限',
    guidance: '请减小请求或预览范围，或联系管理员确认大小限制；重复请求不会解除限制。',
  },
  429: {
    title: '请求过于频繁',
    guidance: '请稍候再手动重试读取；页面不会自动重试。',
  },
  507: {
    title: '存储配额不足',
    guidance: '请联系管理员确认租户或桶配额；重复请求不会增加可用配额。',
  },
}

// Diagnostics are text, never markup. Withhold whole unsafe messages rather
// than risk partially redacting credentials, signed URLs or capability links.
const unsafeDetails = [
  /<[^>]*>/,
  /(?:https?:)?\/\//i,
  /\/(?:share|public|auth)\/\S+/i,
  /[?&][^\s=]+=/,
  /\b(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key|x-aero-share-password)\b\s*["']?\s*[:=]/i,
  /\b(?:bearer|basic)\s+\S+/i,
  /(?:token|password|passwd|secret|signature|credential|api[-_]?key|capability)\s*["']?\s*[:=]/i,
  /\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/,
  /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/,
]

function unsafeDiagnostic(value: string): boolean {
  return unsafeDetails.some((pattern) => pattern.test(value))
}

function diagnosticMessage(message: string): string {
  if (message.length > 1000 || unsafeDiagnostic(message)) {
    return '详细信息已隐藏（可能包含页面内容或敏感信息）。'
  }
  return message
}

function diagnosticRequestId(requestId: string): string {
  return requestId.length <= 128 && !/[\r\n\t]/.test(requestId) && !unsafeDiagnostic(requestId) ? requestId : ''
}

function guidanceFor(status: number): ReadFailureGuidance {
  return httpFailures[status] ?? (status >= 500 && status < 600 ? serviceUnavailable : genericFailure)
}

export function presentReadError(error: Error): (ReadFailureGuidance & { message: string; requestId: string }) | undefined {
  if (error.name === 'AbortError') return undefined
  const apiError = error instanceof VaultApiError ? error : undefined
  return {
    ...(apiError ? guidanceFor(apiError.status) : genericFailure),
    message: diagnosticMessage(error.message),
    requestId: apiError ? diagnosticRequestId(apiError.requestId) : '',
  }
}
