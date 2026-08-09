import { BadgeCheck, KeyRound, RefreshCw, Save, ShieldCheck, ShieldQuestion } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { enabledArtworkModels } from './artwork-options.js';

export function ArtworkSettingsCard({ gateway, registry, operation, onSave, onVerify }) {
  const [draft, setDraft] = useState(() => gatewayDraft(gateway));
  useEffect(() => setDraft(gatewayDraft(gateway)), [gateway]);
  const enabledModels = useMemo(() => enabledArtworkModels(registry), [registry]);
  const busy = operation === 'gateway-save' || operation === 'gateway-verify';
  const update = (patch) => setDraft((current) => ({ ...current, ...patch }));

  async function save(event) {
    event.preventDefault();
    const saved = await onSave(draft);
    if (saved) setDraft(gatewayDraft(saved));
  }

  async function verify() {
    const verified = await onVerify(draft);
    if (verified) setDraft((current) => ({ ...current, api_key: '' }));
  }

  return <aside className="artwork-settings-card" aria-labelledby="artwork-gateway-title">
    <div className="artwork-section-heading">
      <div><span className="artwork-kicker">单一图像网关</span><h3 id="artwork-gateway-title">AI2API 设置</h3></div>
      <span className={`artwork-secret-state ${gateway?.has_api_key ? 'ready' : ''}`}><KeyRound size={14} />{gateway?.has_api_key ? '密钥已保存' : '尚无密钥'}</span>
    </div>
    <form className="artwork-settings-form" onSubmit={save}>
      <label><span>Base URL</span><input aria-label="AI2API Base URL" disabled={busy} onChange={(event) => update({ base_url: event.target.value })} value={draft.base_url} /></label>
      <label><span>API Key</span><input
        aria-label="AI2API API Key"
        autoComplete="new-password"
        disabled={busy || draft.clear_api_key}
        onChange={(event) => update({ api_key: event.target.value, clear_api_key: false })}
        placeholder={gateway?.has_api_key ? '已保存；留空将保持不变' : '仅在保存或验证时发送'}
        type="password"
        value={draft.api_key}
      /></label>
      <label className="artwork-check-row"><input
        checked={draft.clear_api_key}
        disabled={busy || !gateway?.has_api_key}
        onChange={(event) => update({ clear_api_key: event.target.checked, api_key: '' })}
        type="checkbox"
      /><span>明确清除已保存密钥</span></label>
      <label><span>默认图像模型</span><select aria-label="默认图像模型" disabled={busy || !enabledModels.length} onChange={(event) => update({ default_model: event.target.value })} value={draft.default_model}>
        {enabledModels.map((model) => <option key={model.id} value={model.id}>{model.label}</option>)}
      </select></label>
      <label><span>请求超时（秒）</span><input aria-label="图像请求超时" disabled={busy} max="600" min="1" onChange={(event) => update({ request_timeout_seconds: event.target.value })} type="number" value={draft.request_timeout_seconds} /></label>
      <p className="artwork-safety-copy">密钥只写入本机配置，界面和响应都不会回显。验证仅发现模型，不会提交图片生成。</p>
      <div className="artwork-button-row">
        <button className="artwork-button subtle" disabled={busy} onClick={verify} type="button"><RefreshCw className={operation === 'gateway-verify' ? 'artwork-spin' : ''} size={16} />验证并发现</button>
        <button className="artwork-button primary" disabled={busy} type="submit"><Save size={16} />保存设置</button>
      </div>
    </form>
    <div className="artwork-model-catalog" aria-label="图像模型能力状态">
      {(registry?.models || []).map((model) => <div className={!model.enabled ? 'disabled' : ''} key={model.id}>
        <span><strong>{model.label}</strong><small>{model.supported_sizes?.length || 0} 种尺寸</small></span>
        {model.verified ? <em className="verified"><BadgeCheck size={13} />已验证</em> : <em><ShieldQuestion size={13} />未验证</em>}
      </div>)}
      {!registry?.models?.length ? <div className="artwork-empty-inline"><ShieldCheck size={15} />正在读取模型能力…</div> : null}
    </div>
  </aside>;
}

function gatewayDraft(gateway = {}) {
  return {
    base_url: gateway?.base_url || '',
    api_key: '',
    clear_api_key: false,
    default_model: gateway?.default_model || '',
    request_timeout_seconds: String(gateway?.request_timeout_seconds || 120)
  };
}
