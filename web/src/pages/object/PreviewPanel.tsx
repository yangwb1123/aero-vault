import * as React from 'react'
import { IrisBadge, IrisCard } from '@iris-ui-kit/react'
import type { ObjectPreview } from '../../api/objects'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'

interface PreviewData extends ObjectPreview {
  text?: string
}

export function PreviewPanel({ client, objectKey, versionID }: { client: VaultClient; objectKey: string; versionID?: string }): React.ReactElement {
  const controller = React.useRef<AbortController>()
  const load = React.useCallback(async (): Promise<PreviewData> => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    const preview = versionID === undefined ? await client.previewObject(objectKey, request.signal)
      : await client.previewVersion(objectKey, versionID, request.signal)
    return isText(preview.contentType) ? { ...preview, text: await preview.body.text() } : preview
  }, [client, objectKey, versionID])
  const resource = useResource(load)
  React.useEffect(() => () => controller.current?.abort(), [load])

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  if (!resource.data) return <></>
  const preview = resource.data
  return (
    <IrisCard
      variant="outline"
      header={<div className="preview-heading"><strong>{versionID === undefined ? '内容预览' : '历史版本预览'}</strong><div>{versionID !== undefined ? <IrisBadge>Version ID：{versionID}</IrisBadge> : null}<IrisBadge>{preview.contentType || 'application/octet-stream'}</IrisBadge>{preview.partial ? <IrisBadge tone="warning">前 4 KiB</IrisBadge> : null}</div></div>}
    >
      {preview.contentType.startsWith('image/') && !preview.partial ? <ImagePreview blob={preview.body} alt={objectKey} /> : preview.text !== undefined ? <pre className="object-preview-text">{preview.text || '（空文件）'}</pre> : <p className="muted">此内容类型不进行内联渲染，请使用下载操作查看完整文件。JPEG 会通过受限缩略图端点展示。</p>}
    </IrisCard>
  )
}

function ImagePreview({ blob, alt }: { blob: Blob; alt: string }): React.ReactElement {
  const [url, setURL] = React.useState('')
  React.useEffect(() => {
    const next = URL.createObjectURL(blob)
    setURL(next)
    return () => URL.revokeObjectURL(next)
  }, [blob])
  return url ? <img className="object-preview-image" src={url} alt={alt} /> : <></>
}

function isText(contentType: string): boolean {
  return contentType.startsWith('text/') || contentType.includes('json') ||
    contentType.includes('xml') || contentType.includes('javascript')
}
