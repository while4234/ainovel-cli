import { useEffect, useMemo, useState } from 'react';

const WINDOW_PARAGRAPHS = 120;

export function ManuscriptReader({ chapter, busy, error, onMore, onRetry }) {
  const [windowStart, setWindowStart] = useState(0);
  useEffect(() => setWindowStart(0), [chapter?.stable_id, chapter?.view, chapter?.content_signature]);
  const paragraphs = chapter?.paragraphs || [];
  const totalParagraphs = Number(chapter?.total_paragraphs) || paragraphs.length;
  const visible = useMemo(() => paragraphs.slice(windowStart, windowStart + WINDOW_PARAGRAPHS), [paragraphs, windowStart]);
  if (error && !chapter) return <div role="alert">{error}<button type="button" onClick={onRetry}>重试</button></div>;
  return <article className="manuscript-reader" aria-busy={busy}>
    {error ? <div role="alert">网络异常，保留上次成功正文。<button type="button" onClick={onRetry}>重试</button></div> : null}
    <div className="manuscript-window-status" aria-live="polite">
      <span>显示第 {paragraphs.length ? windowStart + 1 : 0}–{Math.min(windowStart + WINDOW_PARAGRAPHS, paragraphs.length)} 段</span>
      <strong>{busy ? '正在自动加载' : '已加载'} {paragraphs.length} / {totalParagraphs} 段</strong>
      {chapter?.next_cursor != null && !busy ? <button type="button" onClick={onMore}>继续加载</button> : null}
    </div>
    <div className="manuscript-window-navigation">
      <button type="button" disabled={windowStart <= 0} onClick={() => setWindowStart(Math.max(0, windowStart - WINDOW_PARAGRAPHS))}>上一窗口</button>
      <button type="button" disabled={windowStart + WINDOW_PARAGRAPHS >= paragraphs.length} onClick={() => setWindowStart(windowStart + WINDOW_PARAGRAPHS)}>下一窗口</button>
    </div>
    <div className="manuscript-prose">{visible.map((paragraph, index) => <p data-paragraph-index={windowStart + index + 1} key={`${windowStart + index}-${paragraph.slice(0, 20)}`}>{paragraph}</p>)}</div>
  </article>;
}
