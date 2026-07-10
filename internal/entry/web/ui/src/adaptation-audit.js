export const defaultAdaptationAuditScope = Object.freeze({
  sourceFrom: '1',
  sourceTo: '30',
  targetFrom: '1',
  targetTo: '44'
});

const scopeFields = [
  ['sourceFrom', 'source_from'],
  ['sourceTo', 'source_to'],
  ['targetFrom', 'target_from'],
  ['targetTo', 'target_to']
];

function positiveWholeNumber(value, label) {
  const text = String(value ?? '').trim();
  if (!text) {
    return { ok: true, value: 0 };
  }
  if (!/^\d+$/.test(text) || Number(text) < 1) {
    return { ok: false, error: `${label}必须是正整数` };
  }
  return { ok: true, value: Number(text) };
}

export function buildAdaptationAuditOptions(scope = {}) {
  const options = {};
  for (const [stateKey, requestKey] of scopeFields) {
    const parsed = positiveWholeNumber(scope[stateKey], stateKey);
    if (!parsed.ok) {
      return parsed;
    }
    if (parsed.value > 0) {
      options[requestKey] = parsed.value;
    }
  }
  if (options.source_from && options.source_to && options.source_from > options.source_to) {
    return { ok: false, error: '原著起始章不能大于结束章' };
  }
  if (options.target_from && options.target_to && options.target_from > options.target_to) {
    return { ok: false, error: '改编起始章不能大于结束章' };
  }
  return { ok: true, options };
}

export function normalizedAdaptationAuditReport(report) {
  if (!report || typeof report !== 'object') {
    return null;
  }
  const confirmation = report.confirmation || {};
  return {
    ...report,
    findings: Array.isArray(report.findings) ? report.findings : [],
    metrics: report.metrics || {},
    scope: report.scope || {},
    confirmation: {
      ...confirmation,
      blocking_finding_ids: Array.isArray(confirmation.blocking_finding_ids)
        ? confirmation.blocking_finding_ids.filter(Boolean)
        : []
    }
  };
}

export function buildAdaptationAuditApplyRequest(report, acknowledged) {
  const normalized = normalizedAdaptationAuditReport(report);
  if (!normalized?.confirmation?.required) {
    return { ok: false, error: '当前报告没有需要应用的修复计划' };
  }
  const reportDigest = String(normalized.confirmation.report_digest || normalized.digest || '').trim();
  if (!reportDigest) {
    return { ok: false, error: '审计报告缺少确认摘要，请重新生成报告' };
  }
  const blockingFindingIDs = normalized.confirmation.blocking_finding_ids;
  if (!acknowledged) {
    return { ok: false, error: '请先确认已了解阻塞问题和返工影响' };
  }
  return {
    ok: true,
    confirmation: {
      report_digest: reportDigest,
      decision: 'apply',
      acknowledged_finding_ids: blockingFindingIDs
    }
  };
}

export function adaptationAuditScopeText(scope = {}) {
  const source = scope.source_from || scope.source_to
    ? `原著 ${scope.source_from || '起始'}–${scope.source_to || '末章'}`
    : '原著全部';
  const target = scope.target_from || scope.target_to
    ? `改编 ${scope.target_from || '起始'}–${scope.target_to || '末章'}`
    : '改编全部';
  return `${source} / ${target}`;
}

export function adaptationAuditApplicationText(application) {
  if (!application || typeof application !== 'object') {
    return '';
  }
  const queued = Array.isArray(application.queued_chapters) ? application.queued_chapters : [];
  const affected = Array.isArray(application.affected_chapters) ? application.affected_chapters : [];
  if (queued.length > 0) {
    return `已将 ${queued.length} 个既有章节排入返工队列；请在确认后点击顶部“恢复”执行。`;
  }
  if (affected.length > 0) {
    return `已更新 ${affected.length} 个章节的改编计划；没有正文会被立即改写。`;
  }
  return '修复计划已应用；没有需要返工的既有正文。';
}
