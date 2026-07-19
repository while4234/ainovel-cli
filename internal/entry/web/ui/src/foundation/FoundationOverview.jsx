import { shortSignature } from './foundationModel.js';

export function FoundationOverview({ server, draft, disabled, premiseError, onPremiseChange, onOpenCoreCast }) {
  const runtime = server.activeRevision;
  const planning = server.planningReview;
  return <div className="foundation-overview-grid">
    <section className="foundation-card" aria-labelledby="foundation-overview-target">
      <h3 id="foundation-overview-target">目标 StoryFoundation</h3>
      <dl className="foundation-metrics">
        <Metric label="模式" value={server.mode === 'adaptation' ? '改编' : '原创'} />
        <Metric label="目标 revision" value={server.baseRevision} />
        <Metric label="audit" value={shortSignature(server.baseAuditSignature)} />
        <Metric label="角色" value={draft.characters.length} />
        <Metric label="计划关系" value={draft.relationships.length} />
        <Metric label="世界规则" value={draft.world_rules.length} />
        <Metric label="可编辑" value={server.editable ? '是' : `否：${server.readonlyReason || '未说明'}`} />
        <Metric label="活动修订" value={runtime ? `${runtime.stage} · ${runtime.revision_id || runtime.session_id}` : '无'} />
        <Metric label="规划审核" value={planning ? `${planning.state || planning.status || 'pending'} · rev ${planning.revision || 0}` : '无'} />
        <Metric label="CoreCast" value={server.coreCastConfirmed ? '已确认' : '需要确认'} />
      </dl>
      <label className="foundation-premise-field"><span>目标故事前提</span><textarea aria-invalid={Boolean(premiseError)} disabled={disabled} value={draft.premise} onChange={(event) => onPremiseChange(event.target.value)} />{premiseError ? <small className="field-error">{premiseError}</small> : null}</label>
      {!server.coreCastConfirmed ? <button className="tool-button" type="button" onClick={onOpenCoreCast}>前往现有 CoreCast 确认</button> : null}
    </section>

    {server.mode === 'adaptation' ? <SourceFoundation source={server.sourceFoundation} server={server} /> : null}
  </div>;
}

function SourceFoundation({ source, server }) {
  const dispositions = Array.isArray(server.coreCast?.source_dispositions) ? server.coreCast.source_dispositions : [];
  const members = Array.isArray(server.coreCast?.members) ? server.coreCast.members : [];
  const targetByID = new Map(members.map((member) => [member?.character?.id, member]));
  return <section className="foundation-card source-foundation" aria-labelledby="foundation-overview-source">
    <div className="foundation-card-title">
      <h3 id="foundation-overview-source">SourceFoundation（只读）</h3>
      <span className="foundation-badge readonly">不可写入</span>
    </div>
    <dl className="foundation-metrics">
      <Metric label="source signature" value={shortSignature(source?.source_signature || server.modeSpecific?.source_signature)} />
      <Metric label="来源章节" value={source?.source_chapter_count || 0} />
      <Metric label="来源角色" value={source?.characters?.length || 0} />
      <Metric label="来源规则" value={source?.world_rules?.length || 0} />
    </dl>
    <h4>原著前提</h4><p className="foundation-long-text">{source?.premise || '未提供'}</p>
    <h4>来源主要角色处置与映射</h4>
    {dispositions.length ? <ul className="foundation-read-list">{dispositions.map((item) => <li key={item.source_character_id}>
      <strong>{sourceName(source, item.source_character_id)}</strong>
      <span>{item.action} → {item.target_character_ids?.map((id) => targetByID.get(id)?.character?.name || id).join('、') || '不映射'}</span>
      {item.rationale ? <small>{item.rationale}</small> : null}
    </li>)}</ul> : <p className="muted">暂无已确认的来源角色处置。</p>}
    <h4>原著规则</h4>
    <ul className="foundation-read-list">{(source?.world_rules || []).map((rule, index) => <li key={rule.id || index}><strong>{rule.title || rule.category || `规则 ${index + 1}`}</strong><span>{rule.rule}</span></li>)}</ul>
  </section>;
}

function sourceName(source, id) {
  return source?.characters?.find((character) => character.id === id)?.name || id;
}

function Metric({ label, value }) {
  return <div><dt>{label}</dt><dd>{value ?? '—'}</dd></div>;
}
