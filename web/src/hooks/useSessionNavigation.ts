import * as React from 'react'
import type { VaultClient } from '../api/vault'
import { isSessionObservation, managementForSession } from '../navigation'
import { useResource } from './useResource'

export function useSessionNavigation(client: VaultClient) {
  const loadSession = React.useCallback(async () => {
    const observation = await client.getSession()
    return isSessionObservation(observation) ? observation : undefined
  }, [client])
  const observed = useResource(loadSession)
  const session = !observed.loading && !observed.error ? observed.data : undefined
  const loadOperator = React.useCallback(async () => {
    if (!managementForSession(session, true).operations) return false
    // /session reports the effective request tenant, not the key's tenant="*".
    // Confirm requireOperatorAdmin via an existing read; never infer it from
    // that tenant, a role name, token presence or returned resource references.
    await client.listAdminTenants()
    return true
  }, [client, session])
  const operator = useResource(loadOperator)
  const management = managementForSession(session, !operator.loading && !operator.error && operator.data === true)
  const status = observed.loading ? 'loading' : observed.error ? 'error' : session ? 'ready' : 'unknown'

  return {
    session,
    status,
    management,
    operatorLoading: operator.loading,
    operatorFailed: Boolean(operator.error),
    reload: () => { observed.reload(); operator.reload() },
  }
}
