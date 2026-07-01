import {
  Activity,
  BookOpen,
  CircleDot,
  FileText,
  FileJson,
  ListRestart,
  Play,
  Plus,
  RefreshCw,
  Send,
  Settings,
  SquarePen,
  Upload,
  WandSparkles
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  analyzeAdaptationSource,
  analyzeSimulation,
  continueProject,
  createProject,
  getRuntime,
  getSnapshot,
  importSimulationProfile,
  listProjects,
  resumeProject,
  startAdaptation,
  steerProject,
  uploadAdaptationSource,
  uploadSimulationFiles
} from './api.js';
import { createWorkbenchState, eventStatus, reduceWebEvent } from './events.js';

const eventTypes = ['host_event', 'stream_delta', 'stream_clear', 'snapshot'];

function createSimulationState() {
  return {
    files: [],
    uploadMessage: '',
    analysisStatus: 'idle',
    analysisEvents: [],
    importStatus: 'idle',
    importEvents: [],
    importMessage: '',
    error: ''
  };
}

function createAdaptationState() {
  return {
    sourceFile: null,
    uploadMessage: '',
    analysisStatus: 'idle',
    analysisEvents: [],
    mode: 'chapter',
    brief: '',
    startStatus: 'idle',
    startMessage: '',
    error: ''
  };
}

export default function App() {
  const [runtime, setRuntime] = useState(null);
  const [projects, setProjects] = useState([]);
  const [activeProject, setActiveProject] = useState(null);
  const [workbench, setWorkbench] = useState(createWorkbenchState);
  const [newProjectName, setNewProjectName] = useState('');
  const [composerText, setComposerText] = useState('');
  const [steerText, setSteerText] = useState('');
  const [sideView, setSideView] = useState('status');
  const [simulation, setSimulation] = useState(createSimulationState);
  const [adaptation, setAdaptation] = useState(createAdaptationState);
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
    setSimulation(createSimulationState());
    setAdaptation(createAdaptationState());
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

  const uploadSimulationSources = async (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (!activeProject?.id || files.length === 0) {
      return;
    }
    setBusy(true);
    setSimulation((previous) => ({ ...previous, uploadMessage: '', error: '' }));
    try {
      const data = await uploadSimulationFiles(activeProject.id, files);
      setSimulation((previous) => ({
        ...previous,
        files: data.files || [],
        uploadMessage: data.message || `已上传 ${files.length} 个文件`,
        error: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({ ...previous, error: err.message }));
    } finally {
      setBusy(false);
    }
  };

  const runSimulationAnalysis = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setSimulation((previous) => ({
      ...previous,
      analysisStatus: 'running',
      analysisEvents: [],
      error: ''
    }));
    try {
      const data = await analyzeSimulation(activeProject.id);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setSimulation((previous) => ({
        ...previous,
        analysisStatus: 'done',
        analysisEvents: data.events || [],
        error: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({
        ...previous,
        analysisStatus: 'error',
        analysisEvents: err.data?.events || previous.analysisEvents,
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const importSimulation = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!activeProject?.id || !file) {
      return;
    }
    setBusy(true);
    setSimulation((previous) => ({
      ...previous,
      importStatus: 'running',
      importEvents: [],
      importMessage: '',
      error: ''
    }));
    try {
      const data = await importSimulationProfile(activeProject.id, file);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setSimulation((previous) => ({
        ...previous,
        importStatus: 'done',
        importEvents: data.events || [],
        importMessage: data.imported_file?.name ? `已导入 ${data.imported_file.name}` : '画像已导入',
        error: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({
        ...previous,
        importStatus: 'error',
        importEvents: err.data?.events || previous.importEvents,
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const uploadAdaptation = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!activeProject?.id || !file) {
      return;
    }
    setBusy(true);
    setAdaptation((previous) => ({
      ...previous,
      sourceFile: null,
      uploadMessage: '',
      analysisStatus: 'idle',
      analysisEvents: [],
      startStatus: 'idle',
      startMessage: '',
      error: ''
    }));
    try {
      const data = await uploadAdaptationSource(activeProject.id, file);
      setAdaptation((previous) => ({
        ...previous,
        sourceFile: data.source_file || null,
        uploadMessage: data.message || `已上传 ${file.name}`,
        error: ''
      }));
    } catch (err) {
      setAdaptation((previous) => ({ ...previous, error: err.message }));
    } finally {
      setBusy(false);
    }
  };

  const runAdaptationAnalysis = async () => {
    if (!activeProject?.id || !adaptation.sourceFile?.relative_path) {
      return;
    }
    setBusy(true);
    setAdaptation((previous) => ({
      ...previous,
      analysisStatus: 'running',
      analysisEvents: [],
      startStatus: 'idle',
      startMessage: '',
      error: ''
    }));
    try {
      const data = await analyzeAdaptationSource(activeProject.id, adaptation.sourceFile.relative_path);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setAdaptation((previous) => ({
        ...previous,
        analysisStatus: 'done',
        analysisEvents: data.events || [],
        error: ''
      }));
    } catch (err) {
      setAdaptation((previous) => ({
        ...previous,
        analysisStatus: 'error',
        analysisEvents: err.data?.events || previous.analysisEvents,
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const startAdaptationRun = async () => {
    if (!activeProject?.id || adaptation.analysisStatus !== 'done') {
      return;
    }
    setBusy(true);
    setAdaptation((previous) => ({
      ...previous,
      startStatus: 'running',
      startMessage: '',
      error: ''
    }));
    try {
      const data = await startAdaptation(activeProject.id, adaptation.sourceFile.relative_path, adaptation.mode, adaptation.brief);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setAdaptation((previous) => ({
        ...previous,
        startStatus: 'done',
        startMessage: `${adaptationModeLabel(data.mode)} / ${data.rewrite_policy}`,
        error: ''
      }));
    } catch (err) {
      setAdaptation((previous) => ({
        ...previous,
        startStatus: 'error',
        startMessage: '',
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
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
          <button className={sideView === 'simulate' ? 'active' : ''} onClick={() => setSideView('simulate')} type="button">
            <WandSparkles size={16} />
            画像
          </button>
          <button className={sideView === 'adapt' ? 'active' : ''} onClick={() => setSideView('adapt')} type="button">
            <FileText size={16} />
            改编
          </button>
          <button className={sideView === 'models' ? 'active' : ''} onClick={() => setSideView('models')} type="button">
            <Settings size={16} />
            模型
          </button>
        </div>

        {sideView === 'status' ? (
          <StatusPanel snapshot={snapshot} activeProject={activeProject} onSteer={submitSteer} steerText={steerText} setSteerText={setSteerText} busy={busy} />
        ) : sideView === 'simulate' ? (
          <SimulationPanel
            activeProject={activeProject}
            busy={busy}
            simulation={simulation}
            onUploadSources={uploadSimulationSources}
            onAnalyze={runSimulationAnalysis}
            onImportProfile={importSimulation}
          />
        ) : sideView === 'adapt' ? (
          <AdaptationPanel
            activeProject={activeProject}
            busy={busy}
            adaptation={adaptation}
            setAdaptation={setAdaptation}
            onUploadSource={uploadAdaptation}
            onAnalyze={runAdaptationAnalysis}
            onStart={startAdaptationRun}
          />
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

const adaptationModes = [
  { value: 'chapter', label: 'chapter' },
  { value: 'arc', label: 'arc' },
  { value: 'free', label: 'free' }
];

function AdaptationPanel({ activeProject, busy, adaptation, setAdaptation, onUploadSource, onAnalyze, onStart }) {
  const latestAnalysis = latestSimulationEvent(adaptation.analysisEvents);
  const analyzed = adaptation.analysisStatus === 'done';
  const canAnalyze = Boolean(activeProject && adaptation.sourceFile && !busy && adaptation.analysisStatus !== 'running');
  const canStart = Boolean(activeProject && adaptation.sourceFile?.relative_path && analyzed && !busy && adaptation.brief.trim());
  return (
    <div className="side-content">
      {adaptation.error ? <div className="error-banner compact">{adaptation.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Upload size={17} />
          <span>小说改编</span>
        </div>
        <div className="simulation-actions">
          <label className={`tool-button file-picker full ${!activeProject || busy ? 'disabled' : ''}`}>
            <Upload size={16} />
            上传原文
            <input
              accept=".txt,.md,.markdown,text/plain,text/markdown"
              disabled={!activeProject || busy}
              onChange={onUploadSource}
              type="file"
            />
          </label>
        </div>
        {adaptation.uploadMessage ? <div className="success-note">{adaptation.uploadMessage}</div> : null}
        <div className="file-list">
          {adaptation.sourceFile ? (
            <div className="file-row">
              <span>{adaptation.sourceFile.name}</span>
              <strong>{formatBytes(adaptation.sourceFile.size)}</strong>
            </div>
          ) : (
            <div className="empty-state">暂无上传原文</div>
          )}
        </div>
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <WandSparkles size={17} />
          <span>原文分析</span>
        </div>
        <button className="tool-button accent full-width" disabled={!canAnalyze} onClick={onAnalyze} type="button">
          <WandSparkles size={16} />
          分析
        </button>
        <div className={`workflow-status ${adaptation.analysisStatus}`}>
          <strong>{workflowStatusText(adaptation.analysisStatus)}</strong>
          <span>{latestAnalysis?.message || '等待分析'}</span>
        </div>
        <SimulationEventList events={adaptation.analysisEvents} />
      </section>

      {analyzed ? (
        <section className="simulation-section">
          <div className="section-title">
            <BookOpen size={17} />
            <span>模式与启动</span>
          </div>
          <div className="adapt-mode-grid" role="radiogroup" aria-label="改编模式">
            {adaptationModes.map((mode) => (
              <label className={adaptation.mode === mode.value ? 'adapt-mode active' : 'adapt-mode'} key={mode.value}>
                <input
                  checked={adaptation.mode === mode.value}
                  disabled={busy}
                  name="adapt-mode"
                  onChange={() => setAdaptation((previous) => ({ ...previous, mode: mode.value }))}
                  type="radio"
                  value={mode.value}
                />
                <span>{mode.label}</span>
              </label>
            ))}
          </div>
          <textarea
            className="adapt-brief"
            aria-label="改编方向"
            disabled={busy}
            placeholder="改编方向..."
            value={adaptation.brief}
            onChange={(event) => setAdaptation((previous) => ({ ...previous, brief: event.target.value }))}
          />
          <button className="tool-button accent full-width" disabled={!canStart} onClick={onStart} type="button">
            <Play size={16} />
            Start
          </button>
          <div className={`workflow-status ${adaptation.startStatus}`}>
            <strong>{workflowStatusText(adaptation.startStatus)}</strong>
            <span>{adaptation.startMessage || '等待启动'}</span>
          </div>
        </section>
      ) : null}
    </div>
  );
}

function SimulationPanel({ activeProject, busy, simulation, onUploadSources, onAnalyze, onImportProfile }) {
  const latestAnalysis = latestSimulationEvent(simulation.analysisEvents);
  const latestImport = latestSimulationEvent(simulation.importEvents);
  return (
    <div className="side-content">
      {simulation.error ? <div className="error-banner compact">{simulation.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Upload size={17} />
          <span>仿写画像</span>
        </div>
        <div className="simulation-actions">
          <label className={`tool-button file-picker ${!activeProject || busy ? 'disabled' : ''}`}>
            <Upload size={16} />
            上传语料
            <input
              accept=".txt,.md,.markdown,text/plain,text/markdown"
              disabled={!activeProject || busy}
              multiple
              onChange={onUploadSources}
              type="file"
            />
          </label>
          <button className="tool-button accent" disabled={!activeProject || busy} onClick={onAnalyze} type="button">
            <WandSparkles size={16} />
            分析
          </button>
        </div>
        {simulation.uploadMessage ? <div className="success-note">{simulation.uploadMessage}</div> : null}
        <div className="file-list">
          {simulation.files.length === 0 ? (
            <div className="empty-state">暂无上传语料</div>
          ) : (
            simulation.files.map((file) => (
              <div className="file-row" key={file.name}>
                <span>{file.name}</span>
                <strong>{formatBytes(file.size)}</strong>
              </div>
            ))
          )}
        </div>
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <WandSparkles size={17} />
          <span>分析进度</span>
        </div>
        <div className={`workflow-status ${simulation.analysisStatus}`}>
          <strong>{workflowStatusText(simulation.analysisStatus)}</strong>
          <span>{latestAnalysis?.message || '等待分析'}</span>
        </div>
        <SimulationEventList events={simulation.analysisEvents} />
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <FileJson size={17} />
          <span>加载画像</span>
        </div>
        <label className={`tool-button file-picker full ${!activeProject || busy ? 'disabled' : ''}`}>
          <FileJson size={16} />
          上传 JSON
          <input accept=".json,application/json" disabled={!activeProject || busy} onChange={onImportProfile} type="file" />
        </label>
        <div className={`workflow-status ${simulation.importStatus}`}>
          <strong>{workflowStatusText(simulation.importStatus)}</strong>
          <span>{simulation.importMessage || latestImport?.message || '等待导入'}</span>
        </div>
        <SimulationEventList events={simulation.importEvents} />
      </section>
    </div>
  );
}

function SimulationEventList({ events }) {
  if (!events.length) {
    return null;
  }
  return (
    <div className="simulation-events">
      {events.map((event, index) => (
        <div className={event.error ? 'simulation-event error' : 'simulation-event'} key={`${event.stage}-${index}`}>
          <span>{event.stage || 'event'}</span>
          <strong>{event.current && event.total ? `${event.current}/${event.total}` : ''}</strong>
          <p>{event.error || event.message}</p>
        </div>
      ))}
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

function latestSimulationEvent(events) {
  return events.length ? events[events.length - 1] : null;
}

function workflowStatusText(status) {
  switch (status) {
    case 'running':
      return '进行中';
    case 'done':
      return '已完成';
    case 'error':
      return '出错';
    default:
      return '待处理';
  }
}

function adaptationModeLabel(mode) {
  return adaptationModes.find((item) => item.value === mode)?.label || mode || '-';
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
