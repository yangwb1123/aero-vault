import type { FilePage, VaultClient } from './vault'

export function listNextFilePage(
  client: Pick<VaultClient, 'listFiles'>,
  prefix: string,
  deleted: boolean,
  marker: string,
): Promise<FilePage> {
  return client.listFiles(prefix, deleted, marker)
}
