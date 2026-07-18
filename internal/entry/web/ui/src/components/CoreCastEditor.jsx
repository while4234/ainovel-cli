import { useEffect, useState } from 'react';
import {
  coreCastImportanceOptions,
  coreCastOriginOptions,
  newCoreCastMember,
  normalizeCoreCast,
  setCoreCastDisposition,
  setCoreCastMemberField,
  setCoreCastMemberSourceID,
  sourceDispositionActions
} from '../coreCast.js';

export function CoreCastEditor({ mode = 'normal', value, completion, confirmed, sourceMajorCharacters = [], busy, onSave, onConfirm, onUnconfirm }) {
  const [draft, setDraft] = useState(() => normalizeCoreCast(value, mode));
  const [relationships, setRelationships] = useState('[]');
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    const next = normalizeCoreCast(value, mode);
    setDraft(next);
    setRelationships(JSON.stringify(next.planned_relationships, null, 2));
    setDirty(false);
    setError('');
  }, [mode, value]);

  const changeMember = (index, path, nextValue) => {
    setDraft((current) => setCoreCastMemberField(current, index, path, nextValue));
    setDirty(true);
  };
  const save = async () => {
    try {
      const next = { ...draft, planned_relationships: JSON.parse(relationships || '[]') };
      setError('');
      await onSave(next);
    } catch (err) {
      setError(err?.message || '角色契约 JSON 无效');
    }
  };

  return (
    <section className="cocreate-section core-cast-editor" aria-label="核心角色确认">
      <div className="section-title"><strong>核心角色确认</strong></div>
      {dirty && confirmed ? <div className="warning-note">内容已修改，保存后当前确认立即失效。</div> : null}
      {error ? <div className="error-banner compact">{error}</div> : null}
      {sourceMajorCharacters.length ? <div className="core-cast-source-list"><strong>原著主要角色</strong>{sourceMajorCharacters.map((item) => <span key={item.id}>{item.name} · {item.id}</span>)}</div> : null}
      {draft.members.map((member, index) => (
        <fieldset className="core-cast-member" key={member.character.id || index}>
          <legend>{member.character.name || `角色 ${index + 1}`}</legend>
          <input aria-label={`角色 ${index + 1} ID`} placeholder="稳定 ID" value={member.character.id} onChange={(event) => changeMember(index, 'character.id', event.target.value)} />
          <input aria-label={`角色 ${index + 1} 姓名`} placeholder="姓名" value={member.character.name} onChange={(event) => changeMember(index, 'character.name', event.target.value)} />
          <input placeholder="身份 / 故事职责" value={member.character.role} onChange={(event) => changeMember(index, 'character.role', event.target.value)} />
          <select value={member.importance} onChange={(event) => changeMember(index, 'importance', event.target.value)}>{coreCastImportanceOptions.map((option) => <option key={option}>{option}</option>)}</select>
          <select value={member.origin} onChange={(event) => changeMember(index, 'origin', event.target.value)}>{coreCastOriginOptions.map((option) => <option key={option}>{option}</option>)}</select>
          {mode === 'adapt' ? <div className="core-cast-source-picker"><strong>来源角色映射</strong>{sourceMajorCharacters.map((source) => <label key={source.id}><input type="checkbox" checked={member.source_character_ids.includes(source.id)} onChange={(event) => { setDraft((current) => setCoreCastMemberSourceID(current, index, source.id, event.target.checked)); setDirty(true); }} /> {source.name} · {source.id}</label>)}</div> : null}
          {['mainline_function', 'character.goal', 'character.motivation', 'character.conflict', 'character.arc', 'character.voice', 'inclusion_rationale'].map((path) => (
            <input key={path} placeholder={path.replace('character.', '').replaceAll('_', ' ')} value={path.startsWith('character.') ? member.character[path.slice(10)] : member[path]} onChange={(event) => changeMember(index, path, event.target.value)} />
          ))}
          <input placeholder="traits，逗号分隔" value={member.character.traits.join(', ')} onChange={(event) => changeMember(index, 'character.traits', splitList(event.target.value))} />
          <input placeholder="constraints，逗号分隔" value={member.character.constraints.join(', ')} onChange={(event) => changeMember(index, 'character.constraints', splitList(event.target.value))} />
          <label><input checked={member.no_core_relationships} type="checkbox" onChange={(event) => changeMember(index, 'no_core_relationships', event.target.checked)} /> 暂无核心关系</label>
          <button className="tool-button" disabled={busy} type="button" onClick={() => { setDraft((current) => ({ ...current, members: current.members.filter((_, memberIndex) => memberIndex !== index) })); setDirty(true); }}>删除角色</button>
        </fieldset>
      ))}
      <button className="tool-button" disabled={busy} type="button" onClick={() => { setDraft((current) => ({ ...current, members: [...current.members, newCoreCastMember()] })); setDirty(true); }}>添加核心角色</button>
      <label className="field-label"><span>核心计划关系（JSON）</span><textarea value={relationships} onChange={(event) => { setRelationships(event.target.value); setDirty(true); }} /></label>
      {mode === 'adapt' ? <div className="core-cast-dispositions"><strong>来源角色处置</strong>{sourceMajorCharacters.map((source) => {
        const disposition = draft.source_dispositions.find((item) => item.source_character_id === source.id) || { action: 'keep', target_character_ids: [], rationale: '' };
        const targetIDs = disposition.target_character_ids || [];
        const updateDisposition = (change) => { setDraft((current) => setCoreCastDisposition(current, source.id, change)); setDirty(true); };
        return <fieldset key={source.id}><legend>{source.name} · {source.id}</legend>
          <select aria-label={`${source.name} 处置`} value={disposition.action} onChange={(event) => updateDisposition({ action: event.target.value, target_character_ids: [] })}>{sourceDispositionActions.map((action) => <option key={action}>{action}</option>)}</select>
          {disposition.action === 'exclude' ? <span>不映射到新书角色</span> : disposition.action === 'split' ? <div>{draft.members.map((member) => <label key={member.character.id}><input type="checkbox" checked={targetIDs.includes(member.character.id)} onChange={(event) => updateDisposition({ target_character_ids: event.target.checked ? [...targetIDs, member.character.id] : targetIDs.filter((id) => id !== member.character.id) })} /> {member.character.name || member.character.id}</label>)}</div> : <select aria-label={`${source.name} 目标角色`} value={targetIDs[0] || ''} onChange={(event) => updateDisposition({ target_character_ids: event.target.value ? [event.target.value] : [] })}><option value="">选择目标角色</option>{draft.members.map((member) => <option key={member.character.id} value={member.character.id}>{member.character.name || member.character.id}</option>)}</select>}
          <input placeholder="处置理由" value={disposition.rationale || ''} onChange={(event) => updateDisposition({ rationale: event.target.value })} />
        </fieldset>;
      })}</div> : null}
      {completion?.missing?.length ? <ul className="core-cast-missing">{completion.missing.map((item, index) => <li key={`${item.code}-${item.member_id || item.source_id || index}`}>{item.description}</li>)}</ul> : null}
      <div className="inline-actions">
        <button className="tool-button" disabled={busy || !dirty} type="button" onClick={save}>保存角色契约</button>
        {confirmed ? <button className="tool-button" disabled={busy || dirty} type="button" onClick={onUnconfirm}>取消确认</button> : <button className="tool-button accent" disabled={busy || dirty || !completion?.complete} type="button" onClick={onConfirm}>显式确认</button>}
      </div>
    </section>
  );
}

function splitList(value) {
  return String(value || '').split(/[,，]/).map((item) => item.trim()).filter(Boolean);
}
