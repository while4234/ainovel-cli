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
    expect(body).toContain('setProjectRetrySettings(activeProject.id, modelAttempts, repairAttempts, budgetAttempts, auditAttempts)');
    expect(body).toContain('setGlobalRetrySettings(modelAttempts, repairAttempts, budgetAttempts, auditAttempts)');
    expect(body).toContain('adaptationOutlineAuditRetryMaxAttempts');
  });

  it('shows project stage routes as provider-agnostic model names', () => {
    expect(appSource).toContain("new Set(providers.flatMap((provider) => provider.models || []))");
    expect(appSource).toContain('<span>创作阶段模型</span>');
    expect(appSource).toContain('同名模型只显示一次，系统自动选择后端。');
    expect(appSource).toContain('独立细纲生成与修订使用“详细提纲”模型');
    expect(appSource).toContain("onSwitch(route.role, provider, model)");
    expect(appSource).toContain('<span>Agent 高级路由</span>');
  });
});
