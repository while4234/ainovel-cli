import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8');

describe('ui styles', () => {
  it('keeps disabled library rows opaque during analysis', () => {
    expect(css).toMatch(/\.library-row\s*{[^}]*display:\s*grid;/s);
    expect(css).toMatch(/\.library-row:disabled\s*{[^}]*opacity:\s*1;/s);
  });

  it('keeps the workspace wider while giving the right tool pane room', () => {
    expect(css).toMatch(/grid-template-columns:\s*minmax\(224px,\s*264px\)\s*minmax\(520px,\s*1fr\)\s*minmax\(430px,\s*520px\);/);
  });

  it('keeps document scrolling locked to internal panes', () => {
    expect(css).toMatch(/html,\s*[\r\n]+body,\s*[\r\n]+#root\s*{[^}]*height:\s*100%;[^}]*overflow:\s*hidden;/s);
    expect(css).toMatch(/body\s*{[^}]*overscroll-behavior:\s*none;/s);
    expect(css).toMatch(/\.app-shell\s*{[^}]*height:\s*100dvh;[^}]*overflow:\s*hidden;/s);
    expect(css).not.toMatch(/\.app-shell\s*{[^}]*overflow:\s*auto;/s);
  });

  it('keeps side content inside the pane without horizontal scrolling', () => {
    expect(css).toMatch(/\.side-content\s*{[^}]*overflow-x:\s*hidden;/s);
    expect(css).toMatch(/\.simulation-section,\s*[\r\n]+\.cocreate-section\s*{[^}]*max-width:\s*100%;/s);
    expect(css).toMatch(/\.profile-status span\s*{[^}]*white-space:\s*nowrap;/s);
  });

  it('keeps long co-create drafts from pushing action buttons away', () => {
    expect(css).toMatch(/\.draft-preview\s*{[^}]*overflow:\s*visible;/s);
    expect(css).not.toMatch(/(?:^|\n)\.draft-preview\s*{[^}]*max-height:/s);
    expect(css).toMatch(/\.cocreate-sticky-workspace \.draft-preview\s*{[^}]*max-height:\s*min\(34vh,\s*360px\);[^}]*overflow:\s*auto;/s);
  });

  it('keeps co-create waiting controls compact', () => {
    expect(css).toMatch(/\.cocreate-dialog\s*{[^}]*min-height:\s*280px;/s);
    expect(css).toMatch(/\.cocreate-dialog-suggestions\s*{[^}]*max-height:\s*132px;/s);
    expect(css).toMatch(/\.cocreate-side-suggestions\s*{[^}]*max-height:\s*176px;/s);
    expect(css).toMatch(/\.cocreate-workspace-output\s*{/);
    expect(css).toMatch(/\.cocreate-workspace-message\.assistant,\s*[\r\n]+\.cocreate-workspace-message\.thinking\s*{/);
    expect(css).toMatch(/\.cocreate-workspace-message\s*{[^}]*max-height:\s*min\(42vh,\s*380px\);[^}]*overflow:\s*hidden;/s);
    expect(css).toMatch(/\.cocreate-workspace-message pre\s*{[^}]*overflow:\s*auto;/s);
    expect(css).toMatch(/\.cocreate-workspace-bottom\s*{[^}]*min-height:\s*1px;/s);
    expect(css).toMatch(/\.cocreate-form textarea\s*{[^}]*max-height:\s*min\(24vh,\s*180px\);[^}]*overflow:\s*auto;/s);
    expect(css).toMatch(/\.cocreate-status-compact \.cocreate-actions\s*{[^}]*grid-template-columns:\s*1fr;/s);
  });

  it('keeps planning review revision controls visible and bounded', () => {
    expect(css).toMatch(/\.proposal-review-actions\s*{[^}]*display:\s*grid;[^}]*grid-template-columns:\s*repeat\(auto-fit,\s*minmax\(220px,\s*1fr\)\);/s);
    expect(css).toMatch(/\.planning-revision-controls\s*{[^}]*display:\s*grid;[^}]*min-width:\s*0;/s);
    expect(css).toMatch(/\.planning-revision-controls\.compact \.proposal-revision-textarea\s*{[^}]*min-height:\s*88px;/s);
  });

  it('uses agent chips with one project model editor', () => {
    expect(css).toMatch(/\.agent-chip-list\s*{[^}]*flex-wrap:\s*wrap;/s);
    expect(css).toMatch(/\.model-route-editor\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.model-route-editor select,[\s\S]*?width:\s*100%;/);
    expect(css).not.toMatch(/\.model-route\s*{/);
  });

  it('keeps model management and add controls single-column full width', () => {
    expect(css).toMatch(/\.model-route-list,\s*[\r\n]+\.custom-model-form\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.model-editor-actions,\s*[\r\n]+\.existing-model-actions\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.backend-picker-row\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.model-editor-actions \.tool-button,\s*[\r\n]+\.existing-model-actions \.tool-button\s*{[^}]*width:\s*100%;/s);
  });
});
