import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8');

describe('ui styles', () => {
  it('keeps disabled library rows opaque during analysis', () => {
    expect(css).toMatch(/\.library-row\s*{[^}]*display:\s*grid;/s);
    expect(css).toMatch(/\.library-row:disabled\s*{[^}]*opacity:\s*1;/s);
  });
});
