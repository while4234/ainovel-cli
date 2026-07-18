import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { chapterOptionLabel, filterChapters } from './chapter-search.js';

export function ChapterCombobox({ chapters = [], selectedId = '', disabled = false, onSelect }) {
  const selected = chapters.find((chapter) => chapter.stable_id === selectedId) || null;
  const selectedLabel = chapterOptionLabel(selected);
  const [inputValue, setInputValue] = useState(selectedLabel);
  const [open, setOpen] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef(null);
  const listboxId = useId();
  const optionId = (index) => `${listboxId}-option-${index}`;
  const matches = useMemo(() => filterChapters(chapters, dirty ? inputValue : ''), [chapters, dirty, inputValue]);

  useEffect(() => {
    if (!open) setInputValue(selectedLabel);
  }, [open, selectedLabel]);

  useEffect(() => {
    if (!open || !matches.length) return;
    const selectedIndex = matches.findIndex((chapter) => chapter.stable_id === selectedId);
    setActiveIndex(selectedIndex >= 0 ? selectedIndex : 0);
  }, [open, dirty, selectedId]);

  function close() {
    setOpen(false);
    setDirty(false);
    setInputValue(selectedLabel);
  }

  function choose(chapter) {
    if (!chapter) return;
    onSelect?.(chapter.stable_id);
    setOpen(false);
    setDirty(false);
    setInputValue(chapterOptionLabel(chapter));
  }

  function keyDown(event) {
    if (event.key === 'Escape') {
      if (open) event.preventDefault();
      close();
      return;
    }
    if (event.key === 'Enter') {
      if (!open || !matches.length) return;
      event.preventDefault();
      choose(matches[activeIndex] || matches[0]);
      return;
    }
    let next = activeIndex;
    if (event.key === 'ArrowDown') next = Math.min(activeIndex + 1, matches.length - 1);
    else if (event.key === 'ArrowUp') next = Math.max(activeIndex - 1, 0);
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = matches.length - 1;
    else return;
    event.preventDefault();
    if (!open) setOpen(true);
    setActiveIndex(Math.max(0, next));
    queueMicrotask(() => document.getElementById(optionId(Math.max(0, next)))?.scrollIntoView({ block: 'nearest' }));
  }

  return <div className="chapter-combobox">
    <label htmlFor={`${listboxId}-input`}>选择章节</label>
    <input
      ref={inputRef}
      id={`${listboxId}-input`}
      type="text"
      role="combobox"
      aria-autocomplete="list"
      aria-controls={listboxId}
      aria-expanded={open}
      aria-activedescendant={open && matches.length ? optionId(activeIndex) : undefined}
      autoComplete="off"
      disabled={disabled}
      placeholder="输入 1、第一章或标题"
      value={inputValue}
      onBlur={() => globalThis.setTimeout(() => { if (!document.activeElement?.closest?.('.chapter-combobox')) close(); }, 0)}
      onChange={(event) => { setInputValue(event.target.value); setDirty(true); setOpen(true); setActiveIndex(0); }}
      onFocus={(event) => { setDirty(false); setOpen(true); event.currentTarget.select(); }}
      onKeyDown={keyDown}
    />
    {open ? <div className="chapter-combobox-popover">
      <div id={listboxId} role="listbox" aria-label="章节选择结果">
        {matches.length ? matches.map((chapter, index) => <button
          id={optionId(index)}
          key={chapter.stable_id}
          type="button"
          role="option"
          aria-selected={chapter.stable_id === selectedId}
          className={index === activeIndex ? 'active' : ''}
          onMouseDown={(event) => event.preventDefault()}
          onMouseEnter={() => setActiveIndex(index)}
          onClick={() => choose(chapter)}
        >
          <span>{chapterOptionLabel(chapter)}</span>
          <small>{chapter.state || '未标记'}</small>
        </button>) : <div className="chapter-combobox-empty" role="status">未找到匹配章节</div>}
      </div>
    </div> : null}
  </div>;
}
