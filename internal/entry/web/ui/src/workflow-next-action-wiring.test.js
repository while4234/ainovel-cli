import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

describe('workflow next-action wiring', () => {
  it('routes adaptation confirmation through the visible workflow panel', () => {
    expect(appSource).toMatch(
      /<WorkflowProgressPanel[\s\S]*?onNextAction=\{runWorkflowNextAction\}[\s\S]*?snapshot=\{snapshot\}/
    );
    expect(appSource).toMatch(/case 'confirm_adaptation_proposal':[\s\S]*?confirmAdaptationRun\(\)/);
    expect(appSource).toMatch(/case 'confirm_planning':[\s\S]*?confirmCoCreatePlanningRun\(\)/);
    expect(appSource).toMatch(/case 'resume_project':[\s\S]*?runAction\(resumeProject/);
  });
});
