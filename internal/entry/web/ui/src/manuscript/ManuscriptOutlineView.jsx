export function ManuscriptOutlineView({ artifact, busy }) {
  const outline = artifact?.content;
  return <section aria-busy={busy}><h3>章节提纲</h3>{outline ? <><p>{outline.title}</p><p>{outline.core_event}</p><p>{outline.hook}</p><ul>{(outline.scenes || []).map((scene) => <li key={scene}>{scene}</li>)}</ul><small>提纲已校验</small></> : <p>正在加载已校验的章节提纲…</p>}</section>;
}
