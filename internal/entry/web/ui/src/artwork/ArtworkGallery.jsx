import { CheckCircle2, Download, Expand, ImageOff, RotateCw, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { ArtworkConfirmDialog } from './ArtworkDialogs.jsx';

export function ArtworkGallery({ assets, cursor, operation, onApply, onDelete, onDownload, onLoadMore, onReuse }) {
  const [preview, setPreview] = useState(null);
  const [deleting, setDeleting] = useState(null);
  return <section className="artwork-gallery" aria-labelledby="artwork-gallery-title">
    <div className="artwork-section-heading"><div><span className="artwork-kicker">项目内不可变素材</span><h3 id="artwork-gallery-title">图片图库</h3></div><span className="artwork-status-count">{assets.length}</span></div>
    <div className="artwork-gallery-grid">
      {assets.map((asset) => <article className={asset.applied ? 'applied' : ''} key={asset.id}>
        <button aria-label={`预览图片 ${asset.id}`} className="artwork-image-preview" onClick={() => setPreview(asset)} type="button">
          <img alt={asset.work_type === 'character_portrait' ? '角色肖像' : asset.work_type === 'cover' ? '书籍封面' : '小说插图'} loading="lazy" src={asset.content_url} />
          <span><Expand size={16} />预览</span>
        </button>
        <div className="artwork-asset-copy"><strong>{assetLabel(asset)}</strong><small>{asset.request?.model_id || '图像模型'} · {asset.request?.size || `${asset.width || '?'}×${asset.height || '?'}`}</small>{asset.applied ? <em><CheckCircle2 size={13} />正在使用</em> : null}</div>
        <div className="artwork-asset-actions">
          <button aria-label="下载图片" disabled={Boolean(operation)} onClick={() => onDownload(asset)} title="下载" type="button"><Download size={15} /></button>
          <button aria-label="复用图片参数" disabled={Boolean(operation)} onClick={() => onReuse(asset)} title="复用参数" type="button"><RotateCw size={15} /></button>
          <button aria-label="应用图片" disabled={Boolean(operation) || asset.applied} onClick={() => onApply(asset)} title={asset.applied ? '已应用' : '应用'} type="button"><CheckCircle2 size={15} /></button>
          <button aria-label="删除图片" disabled={Boolean(operation) || asset.applied} onClick={() => setDeleting(asset)} title={asset.applied ? '正在使用的图片受保护' : '删除'} type="button"><Trash2 size={15} /></button>
        </div>
      </article>)}
      {!assets.length ? <div className="artwork-gallery-empty"><ImageOff size={28} /><strong>图库还是空的</strong><span>图片只会在你确认一次生成后出现。</span></div> : null}
    </div>
    {cursor ? <button className="artwork-button subtle gallery-more" disabled={Boolean(operation)} onClick={onLoadMore} type="button">加载更多图片</button> : null}
    {preview ? <div className="artwork-preview-backdrop" onMouseDown={() => setPreview(null)} role="presentation"><div aria-label="图片预览" aria-modal="true" className="artwork-preview-dialog" onMouseDown={(event) => event.stopPropagation()} role="dialog"><img alt="绘境大图预览" src={preview.content_url} /><button className="artwork-button subtle" onClick={() => setPreview(null)} type="button">关闭预览</button></div></div> : null}
    {deleting ? <ArtworkConfirmDialog
      busy={operation === `delete:${deleting.id}`}
      confirmLabel="确认删除图片"
      description="图片文件和图库记录会被删除；已应用图片不会进入此步骤。"
      kind="delete"
      onCancel={() => setDeleting(null)}
      onConfirm={async () => { if (await onDelete(deleting)) setDeleting(null); }}
      title="删除这张图片？"
    /> : null}
  </section>;
}

function assetLabel(asset) {
  return asset.work_type === 'cover' ? '书籍封面' : asset.work_type === 'character_portrait' ? '角色肖像' : '章节插图';
}
