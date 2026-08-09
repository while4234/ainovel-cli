import { AlertCircle, CheckCircle2, Clock3, LoaderCircle } from 'lucide-react';
import { artworkErrorMessage } from './artwork-state.js';

export function ArtworkStatusPanel({ jobs = [], promptJobs = [] }) {
  const rows = [
    ...jobs.map((job) => ({ ...job, kind: '图片' })),
    ...promptJobs.map((job) => ({ ...job, kind: '提示词' }))
  ].sort((a, b) => String(b.updated_at || b.created_at).localeCompare(String(a.updated_at || a.created_at)));
  return <section className="artwork-status-panel" aria-labelledby="artwork-status-title">
    <div className="artwork-section-heading"><div><span className="artwork-kicker">只轮询活动任务</span><h3 id="artwork-status-title">任务状态</h3></div><span className="artwork-status-count">{rows.length}</span></div>
    <div className="artwork-job-list">
      {rows.slice(0, 8).map((job) => <article key={`${job.kind}-${job.id}`}>
        <JobIcon status={job.status} />
        <span><strong>{job.kind} · {jobLabel(job.status)}</strong><small>{job.request?.model_id || job.model?.model || job.work_type || '当前草稿'}</small></span>
        {job.error_code ? <p>{artworkErrorMessage(job.error_code)}</p> : null}
      </article>)}
      {!rows.length ? <div className="artwork-empty-inline"><Clock3 size={16} />还没有任务；生成只会由你的明确操作触发。</div> : null}
    </div>
  </section>;
}

function JobIcon({ status }) {
  if (status === 'queued' || status === 'running') return <LoaderCircle aria-hidden="true" className="artwork-spin" size={17} />;
  if (status === 'succeeded') return <CheckCircle2 aria-hidden="true" className="artwork-job-success" size={17} />;
  return <AlertCircle aria-hidden="true" className="artwork-job-error" size={17} />;
}

function jobLabel(status) {
  return ({ queued: '等待中', running: '处理中', succeeded: '已完成', failed: '失败', interrupted_unknown: '状态未知' })[status] || status;
}
