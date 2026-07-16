import { useEffect, useMemo, useState } from 'react';

const WINDOW_PARAGRAPHS = 120;

export function ManuscriptReader({ chapter, busy, error, onMore, onRetry }) {
  const [windowStart, setWindowStart] = useState(0);
  useEffect(() => setWindowStart(0), [chapter?.stable_id, chapter?.view, chapter?.content_signature]);
  const paragraphs = chapter?.paragraphs || [];
  const visible = useMemo(() => paragraphs.slice(windowStart, windowStart + WINDOW_PARAGRAPHS), [paragraphs, windowStart]);
  if (error && !chapter) return <div role="alert">{error}<button type="button" onClick={onRetry}>重试</button></div>;
  return <article className="manuscript-reader" aria-busy={busy}>
    {error ? <div role="alert">网络异常，保留上次成功正文。<button type="button" onClick={onRetry}>重试</button></div> : null}
    <p className="manuscript-window-status" aria-live="polite">显示第 {paragraphs.length ? windowStart + 1 : 0}–{Math.min(windowStart + WINDOW_PARAGRAPHS, paragraphs.length)} 段，共已加载 {paragraphs.length} 段</p>
    {windowStart > 0 ? <button type="button" onClick={() => setWindowStart(Math.max(0, windowStart - WINDOW_PARAGRAPHS))}>查看前一段</button> : null}
    {visible.map((paragraph, index) => <p key={`${windowStart + index}-${paragraph.slice(0, 20)}`}>{paragraph}</p>)}
    {windowStart + WINDOW_PARAGRAPHS < paragraphs.length ? <button type="button" onClick={() => setWindowStart(windowStart + WINDOW_PARAGRAPHS)}>查看后一段</button> : null}
    {chapter?.next_cursor != null ? <button type="button" onClick={onMore} disabled={busy}>继续加载正文</button> : null}
  </article>;
}
