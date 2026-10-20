import type { VaultClient } from '../../api/vault'
import { useObjectDownload } from '../../hooks/useObjectDownload'

export function useVersionDownload(client: VaultClient, objectKey: string) {
  const transfer = useObjectDownload(client, JSON.stringify(['version', objectKey]))
  return { busy: transfer.pending?.versionID ?? '', error: transfer.error,
    download: (versionID: string) => versionID ? transfer.download({ key: objectKey, versionID }) : Promise.resolve() }
}
