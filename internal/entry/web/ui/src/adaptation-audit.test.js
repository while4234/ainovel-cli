import { describe, expect, it } from 'vitest';
import {
  adaptationAuditApplicationText,
  adaptationAuditScopeText,
  buildAdaptationAuditApplyRequest,
  buildAdaptationAuditOptions,
  defaultAdaptationAuditScope
} from './adaptation-audit.js';

describe('adaptation audit UI contracts', () => {
  it('uses an explicit, bounded audit scope and permits a full-project audit', () => {
    expect(buildAdaptationAuditOptions(defaultAdaptationAuditScope)).toEqual({
      ok: true,
      options: { source_from: 1, source_to: 30, target_from: 1, target_to: 44 }
    });
    expect(buildAdaptationAuditOptions({})).toEqual({ ok: true, options: {} });
    expect(buildAdaptationAuditOptions({ sourceFrom: '30', sourceTo: '1' })).toMatchObject({
      ok: false,
      error: '原著起始章不能大于结束章'
    });
  });

  it('requires an acknowledgement before submitting every blocking finding id', () => {
    const report = {
      digest: 'report-1',
      confirmation: {
        required: true,
        blocking_finding_ids: ['missing-meet', 'missing-kidnap']
      }
    };
    expect(buildAdaptationAuditApplyRequest(report, false)).toMatchObject({ ok: false });
    expect(buildAdaptationAuditApplyRequest(report, true)).toEqual({
      ok: true,
      confirmation: {
        report_digest: 'report-1',
        decision: 'apply',
        acknowledged_finding_ids: ['missing-meet', 'missing-kidnap']
      }
    });
  });

  it('describes scope and queued repair without claiming that prose is already rewritten', () => {
    expect(adaptationAuditScopeText({ source_from: 1, source_to: 30, target_from: 1, target_to: 44 })).toBe('原著 1–30 / 改编 1–44');
    expect(adaptationAuditApplicationText({ queued_chapters: [1, 2] })).toContain('点击顶部“恢复”执行');
    expect(adaptationAuditApplicationText({ queued_chapters: [1, 2] })).not.toContain('已改写正文');
  });
});
