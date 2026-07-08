import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

function extractFunctionBody(source, name) {
  const signature = `const ${name} = async`;
  const signatureStart = source.indexOf(signature);
  expect(signatureStart).toBeGreaterThanOrEqual(0);

  const bodyStart = source.indexOf('{', signatureStart);
  expect(bodyStart).toBeGreaterThanOrEqual(0);

  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index];
    if (char === '{') {
      depth += 1;
    }
    if (char === '}') {
      depth -= 1;
      if (depth === 0) {
        return source.slice(bodyStart + 1, index);
      }
    }
  }

  throw new Error(`Could not find body for ${name}`);
}

describe('project creation flow', () => {
  it('does not depend on model retry settings before creating a project', () => {
    const body = extractFunctionBody(appSource, 'createAndOpen');

    expect(body).toContain('await createProject(newProjectName)');
    expect(body).not.toContain('budgetAttempts');
    expect(body).not.toContain('budgetQualityMaxAttempts');
  });
});
