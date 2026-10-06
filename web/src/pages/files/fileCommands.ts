import type { VaultClient } from '../../api/vault'

/** Scope filesystem commands to the client that started the page action. */
export function createFileCommands(client: VaultClient): {
  createFolder(path: string): Promise<void>
  removeFolder(path: string): Promise<void>
  removeFile(key: string): Promise<void>
  restoreFile(key: string): Promise<void>
} {
  return {
    createFolder: (path) => client.createFolder(path),
    removeFolder: (path) => client.deleteFolder(path),
    removeFile: (key) => client.deleteFile(key),
    restoreFile: (key) => client.restoreObject(key),
  }
}
