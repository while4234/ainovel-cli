import { AlertTriangle, Bot, Check, ImagePlus, Plus, RotateCw, Sparkles, Trash2 } from 'lucide-react';
import { ARTWORK_TYPES, enabledArtworkModels, scopeKindsForType } from './artwork-options.js';
import { draftCanPersist } from './useArtworkStudio.js';

const WORK_TYPE_LABELS = {
  [ARTWORK_TYPES.COVER]: '封面',
  [ARTWORK_TYPES.ILLUSTRATION]: '插图',
  [ARTWORK_TYPES.PORTRAIT]: '角色肖像'
};

export function ArtworkDraftPanel({ studio, onOpenDialog }) {
  const { editor, filteredDrafts, selectedDraftId, autosave, operation, promptModels, selectedPromptModel, state, workTypes, actions } = studio;
  const busy = Boolean(operation);
  const imageModels = enabledArtworkModels(state.registry);
  const selectedImageModel = imageModels.find((model) => model.id === editor?.model_id);

  return <div className="artwork-draft-layout">
    <aside className="artwork-draft-list" aria-label="绘境草稿">
      <div className="artwork-list-heading"><strong>草稿</strong><button aria-label="新建绘境草稿" className="artwork-icon-button" disabled={busy} onClick={actions.startNewDraft} title="新建草稿" type="button"><Plus size={17} /></button></div>
      <div className="artwork-draft-scroll">
        {filteredDrafts.map((draft) => <button
          aria-current={draft.id === selectedDraftId ? 'true' : undefined}
          className={draft.id === selectedDraftId ? 'active' : ''}
          disabled={busy}
          key={draft.id}
          onClick={() => actions.selectDraft(draft.id)}
          type="button"
        ><span><strong>{WORK_TYPE_LABELS[draft.work_type] || draft.work_type}</strong><small>{scopeLabel(draft)}</small></span><em>{draft.prompt_source === 'ai' ? 'AI' : draft.prompt_source === 'reuse' ? '复用' : '手动'}</em></button>)}
        {!filteredDrafts.length ? <p className="artwork-empty-copy">当前模式还没有草稿。先定义范围和提示词，再明确创建。</p> : null}
      </div>
      {state.draftsCursor ? <button className="artwork-button subtle full" disabled={busy} onClick={actions.loadMoreDrafts} type="button">加载更多草稿</button> : null}
    </aside>

    <section className="artwork-draft-editor" aria-labelledby="artwork-draft-title">
      <div className="artwork-section-heading">
        <div><span className="artwork-kicker">{selectedDraftId ? `版本 ${editor?.version || 0}` : '尚未保存'}</span><h3 id="artwork-draft-title">{selectedDraftId ? '当前图片草稿' : '新建图片草稿'}</h3></div>
        {selectedDraftId ? <AutosaveBadge autosave={autosave} onRetry={actions.retryAutosave} /> : null}
      </div>
      <div className="artwork-editor-grid">
        <label><span>作品类型</span><select aria-label="作品类型" disabled={busy || workTypes.length === 1} onChange={(event) => actions.updateEditor({ work_type: event.target.value })} value={editor?.work_type || ''}>
          {workTypes.map((type) => <option key={type} value={type}>{WORK_TYPE_LABELS[type]}</option>)}
        </select></label>
        <ScopeFields catalog={state.catalog} disabled={busy} editor={editor} onChange={actions.updateEditor} />
        <label><span>提示词模型</span><select aria-label="提示词模型" disabled={busy || !promptModels.length} onChange={(event) => actions.changePromptModel(event.target.value)} value={selectedPromptModel.value}>
          {!promptModels.length ? <option value="">项目默认模型不可用</option> : null}
          {promptModels.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select><small>选择会更新本项目默认文本模型；AI 提示词每次只调用一次。</small></label>
        <label><span>图像模型</span><select aria-label="图像模型" disabled={busy || !imageModels.length} onChange={(event) => actions.updateEditor({ model_id: event.target.value })} value={editor?.model_id || ''}>
          {imageModels.map((model) => <option key={model.id} value={model.id}>{model.label}{model.verified ? ' · 已验证' : ' · 未验证'}</option>)}
        </select></label>
        <label><span>尺寸</span><select aria-label="图片尺寸" disabled={busy || !selectedImageModel?.supported_sizes?.length} onChange={(event) => actions.updateEditor({ size: event.target.value })} value={editor?.size || ''}>
          {(selectedImageModel?.supported_sizes || []).map((size) => <option key={size.value} value={size.value}>{size.label || size.value}</option>)}
        </select></label>
      </div>
      <label className="artwork-prompt-field"><span>图片提示词</span><textarea
        aria-label="图片提示词"
        disabled={busy}
        maxLength={65536}
        onChange={(event) => actions.updateEditor({ prompt: event.target.value })}
        placeholder="描述主体、构图、光线、色彩与氛围；不会自动生成图片。"
        rows={9}
        value={editor?.prompt || ''}
      /></label>
      <div className="artwork-prompt-meta">
        <span>{editor?.prompt_source === 'ai' ? <><Bot size={14} />AI 草拟，可继续手改</> : editor?.prompt_source === 'reuse' ? <><RotateCw size={14} />来自不可变图片参数</> : <><Check size={14} />手动提示词</>}</span>
        <span>{String(editor?.prompt || '').length.toLocaleString()} / 65,536</span>
      </div>
      {editor?.is_stale ? <div className="artwork-stale-warning" role="status"><AlertTriangle size={18} /><span><strong>来源内容已变化</strong><small>提示词不会自动改写；确认当前来源后才能继续生成。</small></span><button className="artwork-button warning" disabled={busy} onClick={() => onOpenDialog('stale')} type="button">查看并确认</button></div> : null}
      {!selectedDraftId ? <p className="artwork-safety-copy">创建草稿只保存参数，不会调用提示词模型或图像网关。</p> : null}
      <div className="artwork-primary-actions">
        {selectedDraftId ? <button className="artwork-button subtle" disabled={busy || autosave.failed || !String(editor?.prompt || '').trim()} onClick={actions.runPromptGeneration} type="button"><Sparkles size={17} />AI 草拟提示词（1 次调用）</button> : null}
        {selectedDraftId ? <button className="artwork-button danger-ghost" disabled={busy} onClick={() => onOpenDialog('draft-delete')} type="button"><Trash2 size={16} />删除草稿</button> : null}
        {!selectedDraftId ? <button className="artwork-button primary" disabled={busy || !draftCanPersist(editor)} onClick={actions.createDraft} type="button"><Plus size={17} />创建草稿</button> : null}
        {selectedDraftId ? <button className="artwork-button generate" disabled={busy || autosave.failed || !String(editor?.prompt || '').trim()} onClick={() => onOpenDialog('paid')} type="button"><ImagePlus size={18} />生成 1 张图片</button> : null}
      </div>
    </section>
  </div>;
}

function ScopeFields({ editor, catalog, disabled, onChange }) {
  const kinds = scopeKindsForType(editor?.work_type);
  if (editor?.work_type === ARTWORK_TYPES.COVER) return <label><span>范围</span><input aria-label="封面范围" disabled readOnly value="整本书" /></label>;
  if (editor?.work_type === ARTWORK_TYPES.PORTRAIT) return <label><span>角色</span><select aria-label="肖像角色" disabled={disabled || !catalog.characters.length} onChange={(event) => onChange({ scope: 'character', scope_id: event.target.value })} value={editor?.scope_id || ''}>
    {!catalog.characters.length ? <option value="">暂无已发布角色</option> : null}
    {catalog.characters.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
  </select></label>;
  const entries = editor?.scope === 'volume' ? catalog.volumes : editor?.scope === 'chapter' ? catalog.chapters : [];
  return <>
    <label><span>插图范围</span><select aria-label="插图范围" disabled={disabled} onChange={(event) => onChange({ scope: event.target.value, scope_id: '' })} value={editor?.scope || 'project'}>
      {kinds.map((kind) => <option key={kind} value={kind}>{kind === 'project' ? '整本书' : kind === 'volume' ? '卷' : '章节'}</option>)}
    </select></label>
    {editor?.scope !== 'project' ? <label><span>{editor.scope === 'volume' ? '卷' : '章节'}</span><select aria-label="插图范围条目" disabled={disabled || !entries.length} onChange={(event) => onChange({ scope_id: event.target.value })} value={editor?.scope_id || ''}>
      {!entries.length ? <option value="">暂无可用内容</option> : null}
      {entries.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
    </select></label> : null}
  </>;
}

function AutosaveBadge({ autosave, onRetry }) {
  if (autosave.failed) return <button className="artwork-autosave failed" onClick={onRetry} type="button"><AlertTriangle size={14} />保存失败，手动重试</button>;
  if (autosave.saving) return <span className="artwork-autosave"><RotateCw className="artwork-spin" size={14} />正在保存</span>;
  if (autosave.dirty) return <span className="artwork-autosave"><span className="artwork-dot" />等待自动保存</span>;
  return <span className="artwork-autosave saved"><Check size={14} />已保存</span>;
}

function scopeLabel(draft) {
  if (draft.work_type === ARTWORK_TYPES.COVER || draft.scope === 'project') return '整本书';
  return draft.scope === 'character' ? '角色' : draft.scope === 'volume' ? '卷' : '章节';
}
