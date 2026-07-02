import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8');

describe('ui styles', () => {
  it('keeps disabled library rows opaque during analysis', () => {
    expect(css).toMatch(/\.library-row\s*{[^}]*display:\s*grid;/s);
    expect(css).toMatch(/\.library-row:disabled\s*{[^}]*opacity:\s*1;/s);
  });

  it('gives the right tool pane enough desktop width', () => {
    expect(css).toMatch(/grid-template-columns:\s*minmax\(224px,\s*264px\)\s*minmax\(360px,\s*1fr\)\s*minmax\(500px,\s*620px\);/);
  });

  it('keeps side content inside the pane without horizontal scrolling', () => {
    expect(css).toMatch(/\.side-content\s*{[^}]*overflow-x:\s*hidden;/s);
    expect(css).toMatch(/\.simulation-section,\s*[\r\n]+\.cocreate-section\s*{[^}]*max-width:\s*100%;/s);
    expect(css).toMatch(/\.profile-status span\s*{[^}]*white-space:\s*nowrap;/s);
  });
});
