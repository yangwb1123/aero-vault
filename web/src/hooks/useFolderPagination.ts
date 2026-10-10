import * as React from 'react'
import type { FileListView, FolderItem, FolderListing } from '../api/files'
import type { VaultClient } from '../api/vault'

interface FolderPagingState {
  client: VaultClient
  scopeKey: string
  initial: FolderListing
  listing: FolderListing
  markers: ReadonlySet<string>
  loading: boolean
  error?: string
}

interface RequestFence {
  id: number
  pending: boolean
}

function mergeListings(previous: FolderListing, next: FolderListing): FolderListing {
  const items = new Map<string, FolderItem>()
  for (const item of [...previous.items, ...next.items]) {
    items.set(JSON.stringify([item.type, item.name]), item)
  }
  return { ...next, items: [...items.values()] }
}

async function advanceFolderPage(
  source: FolderPagingState, path: string, view: FileListView,
  fence: React.MutableRefObject<RequestFence>,
  update: React.Dispatch<React.SetStateAction<FolderPagingState | undefined>>,
): Promise<void> {
  const marker = source.listing.next_marker
  if (fence.current.pending || !source.listing.has_more || !marker) return
  const id = ++fence.current.id
  fence.current.pending = true
  const isCurrent = () => fence.current.id === id
  update({ ...source, loading: true, error: undefined })
  try {
    const next = await source.client.listFolder(path, view, marker)
    if (!isCurrent()) return
    if (next.prefix !== source.initial.prefix) throw new Error('目录响应路径不匹配，请重新加载。')
    const markers = new Set([...source.markers, marker])
    const listing = mergeListings(source.listing, next)
    // Treat markers as opaque keys, not locale/UTF-16-sortable strings.
    if (listing.has_more && (!listing.next_marker || markers.has(listing.next_marker))) {
      listing.next_marker = undefined
    }
    update({ ...source, listing, markers, loading: false })
  } catch (reason) {
    if (isCurrent()) update({ ...source, loading: false,
      error: reason instanceof Error ? reason.message : '加载更多目录失败' })
  } finally {
    if (isCurrent()) fence.current.pending = false
  }
}

/** Isolate continuations by client, path/view and the refreshed initial page. */
export function useFolderPagination(
  client: VaultClient, path: string, view: FileListView, initial?: FolderListing,
): { listing?: FolderListing; loading: boolean; error?: string; loadMore(): void } {
  const scopeKey = JSON.stringify([path, view])
  const [state, setState] = React.useState<FolderPagingState>()
  const fence = React.useRef<RequestFence>({ id: 0, pending: false })
  React.useLayoutEffect(() => {
    fence.current.id += 1
    fence.current.pending = false
    setState(undefined)
  }, [client, scopeKey, initial])
  React.useEffect(() => () => { fence.current.id += 1 }, [])
  const current = state?.client === client && state.scopeKey === scopeKey && state.initial === initial
    ? state : undefined
  const listing = current?.listing ?? initial
  const loadMore = () => {
    if (!initial || !listing) return
    void advanceFolderPage({ client, scopeKey, initial, listing,
      markers: current?.markers ?? new Set(), loading: false }, path, view, fence, setState)
  }
  return { listing, loading: current?.loading ?? false, error: current?.error, loadMore }
}
