import { foundationOptions, newFoundationRelationship, normalizeRelationship } from './foundationModel.js';

export function RelationshipEditor({ value, characters, reviewed, disabled, errors = {}, onChange, onReviewedChange }) {
  const update = (index, field, raw) => onChange(value.map((relationship, itemIndex) => itemIndex === index ? normalizeRelationship({ ...relationship, [field]: ['tags', 'constraints'].includes(field) ? splitList(raw) : raw }) : relationship));
  return <section aria-labelledby="foundation-relationship-heading">
    <div className="foundation-section-head"><div><h2 id="foundation-relationship-heading">计划关系</h2><p>这里是写作前的关系计划，不是正文 runtime relationship；本页不会调用 relationship_state API。</p></div>
      <button className="tool-button" disabled={disabled || characters.length < 2} type="button" onClick={() => onChange([...value, newFoundationRelationship()])}>添加计划关系</button>
    </div>
    <label className="foundation-reviewed"><input checked={reviewed} disabled={disabled} type="checkbox" onChange={(event) => onReviewedChange(event.target.checked)} /> 已显式审查关系；当前可为“暂无核心关系”</label>
    {!value.length ? <div className="empty-state">暂无计划关系。勾选上方状态可显式确认“暂无核心关系”。</div> : <div className="foundation-editor-list">{value.map((relationship, index) => <fieldset className="foundation-editor-card" key={relationship.id || index}>
      <legend>{relationship.label || `关系 ${index + 1}`}</legend>
      <label><span>ID（只读）</span><input readOnly value={relationship.id} /></label>
      <SelectField label="起点角色" value={relationship.source_character_id} error={errors[`relationships.${index}.source_character_id`]} disabled={disabled} onChange={(next) => update(index, 'source_character_id', next)}><option value="">选择角色</option>{characters.map((character) => <option key={character.id} value={character.id}>{character.name || character.id}</option>)}</SelectField>
      <SelectField label="终点角色" value={relationship.target_character_id} error={errors[`relationships.${index}.target_character_id`]} disabled={disabled} onChange={(next) => update(index, 'target_character_id', next)}><option value="">选择角色</option>{characters.map((character) => <option key={character.id} value={character.id}>{character.name || character.id}</option>)}</SelectField>
      <SelectField label="方向" value={relationship.direction} disabled={disabled} onChange={(next) => update(index, 'direction', next)}>{foundationOptions.relationshipDirections.map((option) => <option key={option}>{option}</option>)}</SelectField>
      <SelectField label="类型" value={relationship.type} disabled={disabled} onChange={(next) => update(index, 'type', next)}>{foundationOptions.relationshipTypes.map((option) => <option key={option}>{option}</option>)}</SelectField>
      <SelectField label="状态" value={relationship.status} disabled={disabled} onChange={(next) => update(index, 'status', next)}>{foundationOptions.relationshipStatuses.map((option) => <option key={option}>{option}</option>)}</SelectField>
      {['label', 'description', 'since', 'tags', 'constraints'].map((field) => <label key={field}><span>{relationshipLabel(field)}</span><input disabled={disabled} value={Array.isArray(relationship[field]) ? relationship[field].join(', ') : relationship[field]} onChange={(event) => update(index, field, event.target.value)} /></label>)}
      <button className="tool-button danger-ghost" disabled={disabled} type="button" onClick={() => onChange(value.filter((_, itemIndex) => itemIndex !== index))}>删除关系</button>
    </fieldset>)}</div>}
  </section>;
}

function SelectField({ label, value, error, disabled, onChange, children }) { return <label><span>{label}</span><select aria-invalid={Boolean(error)} disabled={disabled} value={value} onChange={(event) => onChange(event.target.value)}>{children}</select>{error ? <small className="field-error">{error}</small> : null}</label>; }
function splitList(value) { return String(value || '').split(/[,，]/).map((item) => item.trim()).filter(Boolean); }
function relationshipLabel(field) { return ({ label: '标签', description: '说明', since: '起始阶段', tags: '标签集（逗号分隔）', constraints: '约束（逗号分隔）' })[field]; }
