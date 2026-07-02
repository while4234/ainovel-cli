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

  it('keeps side content inside the pane without horizontal scrolling', () => {
    expect(css).toMatch(/\.side-content\s*{[^}]*overflow-x:\s*hidden;/s);
    expect(css).toMatch(/\.simulation-section,\s*[\r\n]+\.cocreate-section\s*{[^}]*max-width:\s*100%;/s);
    expect(css).toMatch(/\.profile-status span\s*{[^}]*white-space:\s*nowrap;/s);
  });

  it('stacks project model controls at matching widths', () => {
    expect(css).toMatch(/\.model-route\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.model-route select,[\s\S]*?width:\s*100%;/);
    expect(css).not.toMatch(/\.model-route select:nth-of-type\(2\)/);
  });
});
