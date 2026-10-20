import * as React from 'react'
import type { ACLEntry, PutACLInput, ResourceKind } from '../../api/access'
import type { VaultClient } from '../../api/vault'

interface ACLTarget {
  client: VaultClient
  bucket: string
  key: string
  kind: ResourceKind
}
interface ACLState {
  client: VaultClient
  scopeKey: string
  entries?: ACLEntry[]
  busy: string
  error?: string
}
interface ACLFence {
  client: VaultClient
  scopeKey: string
  id: number
  pending: boolean
  active: boolean
}
type SetState = React.Dispatch<React.SetStateAction<ACLState>>
type Run = (label: string, action: (isCurrent: () => boolean) => Promise<void>) => Promise<void>

export function useACLRules(client: VaultClient, bucket: string, key: string, kind: ResourceKind, autoLoad: boolean) {
  const scopeKey = JSON.stringify([bucket, key, kind])
  const [state, setState] = React.useState<ACLState>({ client, scopeKey, busy: '' })
  const fence = React.useRef<ACLFence>({ client, scopeKey, id: 0, pending: false, active: true })
  React.useLayoutEffect(() => {
    fence.current = { client, scopeKey, id: fence.current.id + 1, pending: false, active: true }
    setState({ client, scopeKey, busy: '' })
    return () => {
      fence.current.id += 1
      fence.current.pending = false
      fence.current.active = false
    }
  }, [client, scopeKey])
  const target = { client, bucket, key, kind }
  const run = createACLRunner(target, scopeKey, fence, setState)
  const saveEntries = (entries: ACLEntry[], isCurrent: () => boolean) =>
    setState((current) => isCurrent() ? { ...current, entries } : current)
  const operations = createACLOperations(target, run, saveEntries)
  React.useEffect(() => {
    if (autoLoad) void operations.load()
  }, [client, scopeKey, autoLoad])
  const current = state.client === client && state.scopeKey === scopeKey ? state : undefined
  return { entries: current?.entries, busy: current?.busy ?? '', error: current?.error, ...operations }
}

function createACLRunner(target: ACLTarget, scopeKey: string, fence: React.MutableRefObject<ACLFence>, setState: SetState): Run {
  const matches = () => fence.current.active && fence.current.client === target.client && fence.current.scopeKey === scopeKey
  return async (label, action) => {
    if (!matches() || fence.current.pending) return
    const id = ++fence.current.id
    fence.current.pending = true
    const isCurrent = () => matches() && fence.current.id === id
    const patch = (values: Partial<ACLState>) => setState((current) => isCurrent() ? { ...current, ...values } : current)
    patch({ busy: label, error: undefined })
    try {
      await action(isCurrent)
    } catch (reason) {
      if (isCurrent()) {
        const detail = reason instanceof Error ? reason.message : 'ACL 操作失败'
        const caution = label === 'list' ? '' : '；变更可能已生效，请重新读取规则确认。'
        patch({ error: detail + caution })
      }
    } finally {
      if (isCurrent()) {
        fence.current.pending = false
        patch({ busy: '' })
      }
    }
  }
}

function createACLOperations(target: ACLTarget, run: Run, saveEntries: (entries: ACLEntry[], isCurrent: () => boolean) => void) {
  const refresh = async (isCurrent: () => boolean) => {
    const entries = await target.client.listACL(target.bucket, target.key, target.kind)
    if (isCurrent()) saveEntries(entries, isCurrent)
  }
  return {
    load: () => run('list', refresh),
    grant: (input: PutACLInput) => run('grant', async (isCurrent) => {
      if (input.actions.length === 0) throw new Error('至少填写一个授权 action')
      await target.client.putACL({ ...input, bucket: target.bucket, key: target.key, resource_kind: target.kind })
      if (isCurrent()) await refresh(isCurrent)
    }),
    remove: (id: string) => run(`delete:${id}`, async (isCurrent) => {
      await target.client.deleteACL(id)
      if (isCurrent()) await refresh(isCurrent)
    }),
  }
}
