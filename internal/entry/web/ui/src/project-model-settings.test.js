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

describe('project model settings panel', () => {
  it('saves retry settings through the project endpoint when a project is active', () => {
    const body = extractFunctionBody(appSource, 'changeRetrySettings');

    expect(body).toContain('activeProject?.id');
    expect(body).toContain('setProjectRetrySettings(activeProject.id, modelAttempts, repairAttempts, budgetAttempts)');
    expect(body).toContain('setGlobalRetrySettings(modelAttempts, repairAttempts, budgetAttempts)');
  });
});
