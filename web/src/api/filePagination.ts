import type { FileListView } from './files'
import type { FilePage, VaultClient } from './vault'

export function listNextFilePage(
  client: Pick<VaultClient, 'listFiles'>,
  prefix: string,
  deleted: boolean,
  marker: string,
  view: FileListView = 'all',
): Promise<FilePage> {
  return view === 'all'
    ? client.listFiles(prefix, deleted, marker)
    : client.listFiles(prefix, deleted, marker, view)
}
