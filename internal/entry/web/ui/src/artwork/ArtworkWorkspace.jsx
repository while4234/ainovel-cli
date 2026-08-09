import { ArrowLeft, Images, RefreshCw, Sparkles, UserRound } from 'lucide-react';
import { useCallback, useState } from 'react';
import { ArtworkConfirmDialog } from './ArtworkDialogs.jsx';
import { ArtworkDraftPanel } from './ArtworkDraftPanel.jsx';
import { ArtworkGallery } from './ArtworkGallery.jsx';
import { ARTWORK_VIEWS } from './artwork-options.js';
import { ArtworkSettingsCard } from './ArtworkSettingsCard.jsx';
import { ArtworkStatusPanel } from './ArtworkStatusPanel.jsx';
import { useArtworkStudio } from './useArtworkStudio.js';
import './artwork.css';

export function ArtworkWorkspace({
  active = true,
  projectId,
  projectName = '',
  modelConfig = null,
  runtime = null,
  onPromptModelChange,
  onDirtyChange,
  onClose
}) {
  const studio = useArtworkStudio({ projectId, modelConfig, runtime, onPromptModelChange, onDirtyChange });
  const [dialog, setDialog] = useState('');
  const { state, editor, operation, errorMessage, actions } = studio;
  const close = useCallback(async () => {
    try {
      await actions.flushAutosave();
      onClose?.();
    } catch {
      // The autosave controller already publishes a safe actionable error.
    }
  }, [actions, onClose]);
  if (!active) return null;

  return <section className="artwork-workspace" aria-label="绘境视觉工作台">
    <header className="artwork-header">
      <div className="artwork-title-lockup">
        <button aria-label="返回创作工作台" className="artwork-icon-button" onClick={close} type="button"><ArrowLeft size={19} /></button>
        <span className="artwork-title-mark"><Sparkles size={21} /></span>
        <div><span className="artwork-kicker">{projectName || projectId}</span><h2>绘境 · Visual Studio</h2></div>
      </div>
      <div className="artwork-header-actions">
        <div aria-label="绘境模式" className="artwork-view-tabs" role="tablist">
          <button aria-selected={studio.view === ARTWORK_VIEWS.STORY} onClick={() => actions.changeView(ARTWORK_VIEWS.STORY)} role="tab" type="button"><Images size={16} />故事视觉</button>
          <button aria-selected={studio.view === ARTWORK_VIEWS.CHARACTER} onClick={() => actions.changeView(ARTWORK_VIEWS.CHARACTER)} role="tab" type="button"><UserRound size={16} />角色肖像</button>
        </div>
        <button aria-label="刷新绘境工作台" className="artwork-icon-button" disabled={Boolean(operation)} onClick={actions.refreshWorkspace} title="刷新" type="button"><RefreshCw size={18} /></button>
      </div>
    </header>

    {state.status === 'loading' ? <div className="artwork-loading" role="status"><RefreshCw className="artwork-spin" size={18} />正在恢复项目草稿、任务与图库…</div> : null}
    {errorMessage ? <div className="artwork-message error" role="alert">{errorMessage}</div> : null}
    {state.notice ? <div className="artwork-message success" aria-live="polite" role="status">{state.notice}</div> : null}

    {editor ? <div className="artwork-body">
      <main className="artwork-main-column">
        <ArtworkDraftPanel onOpenDialog={setDialog} studio={studio} />
        <ArtworkStatusPanel jobs={state.jobs} promptJobs={state.promptJobs} />
        <ArtworkGallery
          assets={state.assets}
          cursor={state.assetsCursor}
          onApply={actions.applyAsset}
          onDelete={actions.removeAsset}
          onDownload={actions.downloadAsset}
          onLoadMore={actions.loadMoreAssets}
          onReuse={actions.reuseAsset}
          operation={operation}
        />
      </main>
      <ArtworkSettingsCard gateway={state.gateway} onSave={actions.saveGateway} onVerify={actions.verifyGateway} operation={operation} registry={state.registry} />
    </div> : null}

    {dialog === 'paid' ? <ArtworkConfirmDialog
      busy={operation === 'image'}
      confirmLabel="确认并生成 1 张"
      description="这会向已配置的 AI2API 网关提交一次、仅一张图片的请求，可能产生费用。系统不会自动追加、批量生成或失败重试。"
      kind="paid"
      onCancel={() => setDialog('')}
      onConfirm={async () => { if (await actions.submitImageGeneration()) setDialog(''); }}
      title="确认一次可能付费的图片生成"
    ><dl className="artwork-confirm-summary"><div><dt>模型</dt><dd>{studio.imageModel?.label || editor?.model_id}</dd></div><div><dt>尺寸</dt><dd>{editor?.size}</dd></div><div><dt>数量</dt><dd>1 张</dd></div></dl></ArtworkConfirmDialog> : null}

    {dialog === 'stale' ? <ArtworkConfirmDialog
      busy={operation === 'stale-confirm'}
      confirmLabel="确认使用当前来源"
      description="原始内容在 AI 提示词生成后发生了变化。确认只记录你已知晓当前来源版本，不会自动重写提示词，也不会生成图片。"
      onCancel={() => setDialog('')}
      onConfirm={async () => { if (await actions.confirmStale()) setDialog(''); }}
      title="确认过期提示词来源？"
    /> : null}

    {dialog === 'draft-delete' ? <ArtworkConfirmDialog
      busy={operation === 'draft-delete'}
      confirmLabel="确认删除草稿"
      description="草稿和提示词版本会被删除；已经生成的图片仍会保留在项目图库。"
      kind="delete"
      onCancel={() => setDialog('')}
      onConfirm={async () => { if (await actions.removeDraft()) setDialog(''); }}
      title="删除当前草稿？"
    /> : null}
  </section>;
}

export default ArtworkWorkspace;
