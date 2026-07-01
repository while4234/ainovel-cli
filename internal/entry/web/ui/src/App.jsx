import {
  Activity,
  BookOpen,
  CircleDot,
  ListRestart,
  Play,
  Plus,
  RefreshCw,
  Send,
  Settings,
  SquarePen
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  continueProject,
  createProject,
  getRuntime,
  getSnapshot,
  listProjects,
  resumeProject,
  steerProject
} from './api.js';
import { createWorkbenchState, eventStatus, reduceWebEvent } from './events.js';

const eventTypes = ['host_event', 'stream_delta', 'stream_clear', 'snapshot'];

export default function App() {
  const [runtime, setRuntime] = useState(null);
  const [projects, setProjects] = useState([]);
  const [activeProject, setActiveProject] = useState(null);
  const [workbench, setWorkbench] = useState(createWorkbenchState);
  const [newProjectName, setNewProjectName] = useState('');
  const [composerText, setComposerText] = useState('');
  const [steerText, setSteerText] = useState('');
  const [sideView, setSideView] = useState('status');
  const [connection, setConnection] = useState('idle');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const lastSeqRef = useRef(0);

  const snapshot = workbench.snapshot;

  const refreshProjects = useCallback(async () => {
    const data = await listProjects();
    setProjects(data.projects || []);
  }, []);

  const loadShell = useCallback(async () => {
    setError('');
    try {
      const [runtimeData, projectsData] = await Promise.all([getRuntime(), listProjects()]);
      setRuntime(runtimeData);
      setProjects(projectsData.projects || []);
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    loadShell();
  }, [loadShell]);

  const openProject = useCallback(async (project) => {
    setBusy(true);
    setError('');
    lastSeqRef.current = 0;
    setWorkbench(createWorkbenchState());
    try {
      const data = await getSnapshot(project.id);
      setActiveProject(data.project);
      setWorkbench({ ...createWorkbenchState(), snapshot: data.snapshot });
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    if (!activeProject?.id) {
      setConnection('idle');
      return undefined;
    }

    let disposed = false;
    let source = null;
    let retryTimer = null;

    const apply = (message) => {
      const event = JSON.parse(message.data);
      setWorkbench((previous) => {
        const next = reduceWebEvent(previous, event);
        lastSeqRef.current = next.lastSeq;
        return next;
      });
      setConnection('live');
    };

    const connect = () => {
      const after = lastSeqRef.current;
      const url = `/api/projects/${encodeURIComponent(activeProject.id)}/events?after=${after}`;
      source = new EventSource(url);
      setConnection(after > 0 ? 'reconnecting' : 'connecting');
      for (const type of eventTypes) {
        source.addEventListener(type, apply);
      }
      source.onerror = () => {
        if (disposed) {
          return;
        }
        setConnection('reconnecting');
        source.close();
        retryTimer = window.setTimeout(connect, 1200);
      };
    };

    connect();
    return () => {
      disposed = true;
      if (retryTimer) {
        window.clearTimeout(retryTimer);
      }
      if (source) {
        source.close();
      }
    };
  }, [activeProject?.id]);

  const createAndOpen = async (event) => {
    event.preventDefault();
    setBusy(true);
    setError('');
    try {
      const project = await createProject(newProjectName);
      setNewProjectName('');
      await refreshProjects();
      await openProject(project);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const runAction = async (action) => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await action(activeProject.id);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const submitContinue = async (event) => {
    event.preventDefault();
    const text = composerText.trim();
    if (!text) {
      return;
    }
    await runAction((projectId) => continueProject(projectId, text));
    setComposerText('');
  };

  const submitSteer = async (event) => {
    event.preventDefault();
    const text = steerText.trim();
    if (!text) {
      return;
    }
    await runAction((projectId) => steerProject(projectId, text));
    setSteerText('');
  };

  const sortedEvents = useMemo(
    () => workbench.eventRows.slice().sort((a, b) => b.seq - a.seq),
    [workbench.eventRows]
  );

  return (
    <div className="app-shell">
      <aside className="project-pane">
        <div className="pane-header">
          <div>
            <div className="eyebrow">ainovel</div>
            <h1>小说工作台</h1>
          </div>
          <button className="icon-button" onClick={loadShell} title="刷新项目列表" type="button">
            <RefreshCw size={18} />
          </button>
        </div>

        <form className="create-project" onSubmit={createAndOpen}>
          <input
            aria-label="新项目名称"
            placeholder="新小说项目"
            value={newProjectName}
            onChange={(event) => setNewProjectName(event.target.value)}
          />
          <button className="icon-button primary" disabled={busy} title="创建项目" type="submit">
            <Plus size={18} />
          </button>
        </form>

        <div className="project-list" aria-label="项目列表">
          {projects.length === 0 ? (
            <div className="empty-state">暂无项目</div>
          ) : (
            projects.map((project) => (
              <button
                key={project.id}
                className={project.id === activeProject?.id ? 'project-row active' : 'project-row'}
                onClick={() => openProject(project)}
                type="button"
              >
                <BookOpen size={17} />
                <span>
                  <strong>{project.name || project.id}</strong>
                  <small>{formatDate(project.last_accessed_at || project.created_at)}</small>
                </span>
              </button>
            ))
          )}
        </div>
      </aside>

      <main className="writing-pane">
        <header className="workspace-toolbar">
          <div>
            <div className="eyebrow">当前项目</div>
            <h2>{activeProject?.name || '未打开项目'}</h2>
          </div>
          <div className="toolbar-actions">
            <StatusPill status={connection} />
            <button
              className="tool-button"
              disabled={!activeProject || busy}
              onClick={() => openProject(activeProject)}
              type="button"
            >
              <ListRestart size={16} />
              Snapshot
            </button>
            <button
              className="tool-button accent"
              disabled={!activeProject || busy}
              onClick={() => runAction(resumeProject)}
              type="button"
            >
              <Play size={16} />
              Resume
            </button>
          </div>
        </header>

        {error ? <div className="error-banner">{error}</div> : null}

        <section className="stream-area" aria-label="实时创作流">
          {activeProject ? (
            workbench.streamRounds.map((round) => (
              <article className="stream-round" key={round.id}>
                {round.text ? <pre>{round.text}</pre> : <span className="muted">等待流式输出</span>}
              </article>
            ))
          ) : (
            <div className="no-project">
              <SquarePen size={28} />
              <strong>打开或创建一本小说</strong>
              <span>项目列表和模型配置随时可用。</span>
            </div>
          )}
        </section>

        <section className="event-feed" aria-label="运行事件">
          <div className="section-title">
            <Activity size={17} />
            <span>事件</span>
          </div>
          <div className="event-list">
            {sortedEvents.length === 0 ? (
              <div className="empty-state">暂无事件</div>
            ) : (
              sortedEvents.map((event) => (
                <div className={`event-row ${eventStatus(event)}`} key={event.host_event_id || event.seq}>
                  <span className="event-dot" />
                  <span className="event-time">{formatTime(event.time)}</span>
                  <strong>{event.event?.category || 'EVENT'}</strong>
                  <span>{event.event?.summary || '无摘要'}</span>
                </div>
              ))
            )}
          </div>
        </section>

        <form className="composer" onSubmit={submitContinue}>
          <input
            aria-label="继续创作输入"
            disabled={!activeProject || busy}
            placeholder="继续、补充或要求下一步..."
            value={composerText}
            onChange={(event) => setComposerText(event.target.value)}
          />
          <button className="tool-button accent" disabled={!activeProject || busy} type="submit">
            <Send size={16} />
            Continue
          </button>
        </form>
      </main>

      <aside className="status-pane">
        <div className="side-tabs">
          <button className={sideView === 'status' ? 'active' : ''} onClick={() => setSideView('status')} type="button">
            <CircleDot size={16} />
            状态
          </button>
          <button className={sideView === 'models' ? 'active' : ''} onClick={() => setSideView('models')} type="button">
            <Settings size={16} />
            模型
          </button>
        </div>

        {sideView === 'status' ? (
          <StatusPanel snapshot={snapshot} activeProject={activeProject} onSteer={submitSteer} steerText={steerText} setSteerText={setSteerText} busy={busy} />
        ) : (
          <ModelPanel runtime={runtime} />
        )}
      </aside>
    </div>
  );
}

function StatusPanel({ snapshot, activeProject, onSteer, steerText, setSteerText, busy }) {
  const outline = snapshot?.Outline || snapshot?.outline || [];
  const agents = snapshot?.Agents || snapshot?.agents || [];
  return (
    <div className="side-content">
      <section className="metric-grid">
        <Metric label="状态" value={snapshot?.RuntimeState || snapshot?.runtime_state || 'idle'} />
        <Metric label="章节" value={`${snapshot?.CompletedCount || 0}/${snapshot?.TotalChapters || 0}`} />
        <Metric label="当前" value={snapshot?.CurrentChapter || snapshot?.InProgressChapter || 0} />
        <Metric label="字数" value={snapshot?.TotalWordCount || 0} />
      </section>

      <form className="steer-form" onSubmit={onSteer}>
        <textarea
          aria-label="干预输入"
          disabled={!activeProject || busy}
          placeholder="运行中即时干预；未运行时保存为下次生效"
          value={steerText}
          onChange={(event) => setSteerText(event.target.value)}
        />
        <button className="tool-button" disabled={!activeProject || busy} type="submit">
          <Send size={16} />
          Steer
        </button>
      </form>

      <section>
        <div className="section-title">
          <BookOpen size={17} />
          <span>章节</span>
        </div>
        <div className="chapter-list">
          {outline.length === 0 ? (
            <div className="empty-state">暂无大纲</div>
          ) : (
            outline.slice(0, 12).map((item) => (
              <div className="chapter-row" key={`${item.Chapter || item.chapter}-${item.Title || item.title}`}>
                <span>{item.Chapter || item.chapter}</span>
                <strong>{item.Title || item.title || '未命名章节'}</strong>
              </div>
            ))
          )}
        </div>
      </section>

      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>Agents</span>
        </div>
        <div className="agent-list">
          {agents.length === 0 ? (
            <div className="empty-state">暂无 agent 状态</div>
          ) : (
            agents.map((agent) => (
              <div className="agent-row" key={agent.Name || agent.name}>
                <strong>{agent.Name || agent.name}</strong>
                <span>{agent.State || agent.state || 'idle'}</span>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

function ModelPanel({ runtime }) {
  const config = runtime?.config || {};
  const roles = Object.entries(config.roles || {});
  return (
    <div className="side-content">
      <section className="model-summary">
        <Metric label="Provider" value={config.provider || '未配置'} />
        <Metric label="Model" value={config.model || '未配置'} />
        <Metric label="Style" value={config.style || 'default'} />
        <Metric label="Runtime" value={runtime?.runtime_root || '-'} />
      </section>
      <section>
        <div className="section-title">
          <Settings size={17} />
          <span>角色模型</span>
        </div>
        <div className="agent-list">
          {roles.length === 0 ? (
            <div className="empty-state">使用默认模型</div>
          ) : (
            roles.map(([role, value]) => (
              <div className="agent-row" key={role}>
                <strong>{role}</strong>
                <span>{value.provider || config.provider}/{value.model || config.model}</span>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

function Metric({ label, value }) {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong title={String(value)}>{String(value)}</strong>
    </div>
  );
}

function StatusPill({ status }) {
  return <span className={`status-pill ${status}`}>{status}</span>;
}

function formatDate(value) {
  if (!value) {
    return '未访问';
  }
  return new Date(value).toLocaleDateString();
}

function formatTime(value) {
  if (!value) {
    return '--:--:--';
  }
  return new Date(value).toLocaleTimeString();
}
