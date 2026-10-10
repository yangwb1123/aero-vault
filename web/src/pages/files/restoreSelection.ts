import type { VaultClient } from '../../api/vault'

export interface RestoreSelectionResult {
  key: string
  restored: boolean
  error?: string
}

/** Restore selected keys serially; a scope change stops only unsent requests. */
export async function restoreSelection(
  client: Pick<VaultClient, 'restoreObject'>,
  keys: readonly string[],
  isCurrent: () => boolean,
): Promise<RestoreSelectionResult[]> {
  const results: RestoreSelectionResult[] = []
  for (const key of new Set(keys)) {
    if (!isCurrent()) break
    try {
      await client.restoreObject(key)
      results.push({ key, restored: true })
    } catch (reason) {
      results.push({ key, restored: false,
        error: reason instanceof Error ? reason.message : '恢复失败' })
    }
  }
  return results
}

export function restoreSelectionMessage(results: RestoreSelectionResult[]): { tone: 'success' | 'danger'; text: string } {
  const failed = results.filter((result) => !result.restored)
  if (!failed.length) return { tone: 'success', text: `已恢复 ${results.length} 个文件。` }
  const reasons = failed.slice(0, 3).map((result) => `${result.key}：${result.error || '恢复失败'}`).join('；')
  return { tone: 'danger',
    text: `已恢复 ${results.length - failed.length} 个文件，${failed.length} 个失败；失败项仍保持选中。${reasons}` }
}
