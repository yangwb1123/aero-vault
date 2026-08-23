import * as React from 'react'
import { IrisBadge, IrisCard } from '@iris-ui-kit/react'
import type { ObjectPreview } from '../../api/objects'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'

interface PreviewData extends ObjectPreview {
  text?: string
}

export function PreviewPanel({ client, objectKey }: { client: VaultClient; objectKey: string }): React.ReactElement {
  const load = React.useCallback(async (): Promise<PreviewData> => {
    const preview = await client.previewObject(objectKey)
    return isText(preview.contentType) ? { ...preview, text: await preview.body.text() } : preview
  }, [client, objectKey])
  const resource = useResource(load)

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  if (!resource.data) return <></>
  const preview = resource.data
  return (
    <IrisCard
      variant="outline"
      header={<div className="preview-heading"><strong>内容预览</strong><div><IrisBadge>{preview.contentType || 'application/octet-stream'}</IrisBadge>{preview.partial ? <IrisBadge tone="warning">前 4 KiB</IrisBadge> : null}</div></div>}
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
