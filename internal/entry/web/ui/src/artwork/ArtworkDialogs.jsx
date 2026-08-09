import { useEffect, useRef } from 'react';
import { AlertTriangle, ImagePlus, Trash2, X } from 'lucide-react';

export function ArtworkConfirmDialog({ kind = 'confirm', title, description, confirmLabel, busy = false, onCancel, onConfirm, children }) {
  const cancelRef = useRef(null);
  const dialogRef = useRef(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const onKeyDown = (event) => {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onCancel();
      }
      if (event.key !== 'Tab') return;
      const buttons = [...(dialogRef.current?.querySelectorAll('button:not(:disabled)') || [])];
      if (!buttons.length) return;
      if (event.shiftKey && document.activeElement === buttons[0]) {
        event.preventDefault();
        buttons.at(-1)?.focus();
      } else if (!event.shiftKey && document.activeElement === buttons.at(-1)) {
        event.preventDefault();
        buttons[0]?.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [busy, onCancel]);
  const Icon = kind === 'paid' ? ImagePlus : kind === 'delete' ? Trash2 : AlertTriangle;
  return <div className="artwork-dialog-backdrop" role="presentation">
    <div
      aria-describedby="artwork-dialog-description"
      aria-labelledby="artwork-dialog-title"
      aria-modal="true"
      className={`artwork-dialog artwork-dialog-${kind}`}
      ref={dialogRef}
      role="alertdialog"
    >
      <header><Icon aria-hidden="true" size={20} /><h3 id="artwork-dialog-title">{title}</h3></header>
      <p id="artwork-dialog-description">{description}</p>
      {children}
      <footer>
        <button className="artwork-button subtle" disabled={busy} onClick={onCancel} ref={cancelRef} type="button"><X size={16} />取消</button>
        <button className={`artwork-button ${kind === 'delete' ? 'danger' : 'primary'}`} disabled={busy} onClick={onConfirm} type="button">
          {busy ? '处理中…' : confirmLabel}
        </button>
      </footer>
    </div>
  </div>;
}
