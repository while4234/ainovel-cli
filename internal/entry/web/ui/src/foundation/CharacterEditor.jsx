import { useEffect, useRef, useState } from 'react';
import { isCoreCharacter, newFoundationCharacter, normalizeCharacter } from './foundationModel.js';

const textFields = [
  ['name', '姓名'], ['aliases', '别名（逗号分隔）'], ['role', '身份 / 故事职责'], ['description', '描述'],
  ['tier', '层级'], ['faction', '阵营'], ['goal', '目标'], ['motivation', '动机'], ['conflict', '冲突'],
  ['arc', '角色弧'], ['traits', '特质（逗号分隔）'], ['voice', '语言风格'], ['constraints', '约束（逗号分隔）'], ['notes', '备注']
];

export function CharacterEditor({ value, coreCast, disabled, errors = {}, onChange }) {
  const [pendingDelete, setPendingDelete] = useState(null);
  const cancelRef = useRef(null);
  const addRef = useRef(null);
  useEffect(() => { if (pendingDelete) cancelRef.current?.focus(); }, [pendingDelete]);

  const update = (index, field, raw) => {
    const characters = value.map((character, itemIndex) => itemIndex === index
      ? normalizeCharacter({ ...character, [field]: listField(field) ? splitList(raw) : raw })
      : character);
    onChange(characters);
  };
  const remove = (index, trigger) => {
    const character = value[index];
    if (isCoreCharacter(character, coreCast)) setPendingDelete({ index, character, trigger });
    else onChange(value.filter((_, itemIndex) => itemIndex !== index));
  };
  const cancelDelete = () => {
    const trigger = pendingDelete?.trigger;
    setPendingDelete(null);
    globalThis.requestAnimationFrame?.(() => trigger?.focus());
  };
  const confirmDelete = () => {
    onChange(value.filter((_, itemIndex) => itemIndex !== pendingDelete.index));
    setPendingDelete(null);
    globalThis.requestAnimationFrame?.(() => addRef.current?.focus());
  };

  return <section aria-labelledby="foundation-character-heading">
    <div className="foundation-section-head"><div><h2 id="foundation-character-heading">全部角色</h2><p>核心角色删除会触发高风险提示；最终影响与是否可应用以服务端 preview 为准。</p></div>
      <button ref={addRef} className="tool-button" disabled={disabled} type="button" onClick={() => onChange([...value, newFoundationCharacter()])}>添加角色</button>
    </div>
    {!value.length ? <div className="empty-state">暂无角色</div> : <div className="foundation-editor-list">{value.map((character, index) => {
      const core = isCoreCharacter(character, coreCast);
      return <fieldset className="foundation-editor-card" key={character.id || index}>
        <legend>{character.name || `角色 ${index + 1}`} {core ? <span className="foundation-badge risk">核心角色</span> : null}</legend>
        <label><span>ID（只读）</span><input readOnly value={character.id} /></label>
        {textFields.map(([field, label]) => <label key={field}><span>{label}</span>{longField(field)
          ? <textarea aria-invalid={Boolean(errors[`characters.${index}.${field}`])} disabled={disabled} value={displayValue(character[field])} onChange={(event) => update(index, field, event.target.value)} />
          : <input aria-invalid={Boolean(errors[`characters.${index}.${field}`])} disabled={disabled} value={displayValue(character[field])} onChange={(event) => update(index, field, event.target.value)} />}
          {errors[`characters.${index}.${field}`] ? <small className="field-error">{errors[`characters.${index}.${field}`]}</small> : null}</label>)}
        <button className="tool-button danger-ghost" disabled={disabled} type="button" onClick={(event) => remove(index, event.currentTarget)}>删除角色</button>
      </fieldset>;
    })}</div>}
    {pendingDelete ? <div className="foundation-dialog-backdrop" role="presentation"><div aria-describedby="foundation-delete-description" aria-labelledby="foundation-delete-title" aria-modal="true" className="foundation-dialog" role="alertdialog">
      <h3 id="foundation-delete-title">删除核心角色？</h3>
      <p id="foundation-delete-description">“{pendingDelete.character.name}”属于当前 CoreCast。删除会要求重新确认 CoreCast，且可能扩大到全书重新审查。</p>
      <div className="inline-actions"><button ref={cancelRef} className="tool-button" type="button" onClick={cancelDelete}>取消</button><button className="tool-button danger" type="button" onClick={confirmDelete}>确认删除</button></div>
    </div></div> : null}
  </section>;
}

function listField(field) { return ['aliases', 'traits', 'constraints'].includes(field); }
function longField(field) { return ['description', 'arc', 'notes'].includes(field); }
function displayValue(value) { return Array.isArray(value) ? value.join(', ') : value || ''; }
function splitList(value) { return String(value || '').split(/[,，]/).map((item) => item.trim()).filter(Boolean); }
