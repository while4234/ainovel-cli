import {
  Activity,
  BookOpen,
  Check,
  CircleDot,
  Database,
  Download,
  FileText,
  FileJson,
  ListRestart,
  MessageSquareText,
  MoreHorizontal,
  PauseCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Send,
  Server,
  Settings,
  SlidersHorizontal,
  SquarePen,
  TestTube2,
  Trash2,
  Upload,
  WandSparkles,
  X
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  analyzeAdaptationSource,
  analyzeSimulation,
  addProviderModel,
  beginCoCreate,
  cancelCoCreate,
  commitCoCreate,
  completeGrokLogin,
  continueProject,
  createProject,
  exportProject,
  getBackendStatus,
  getGlobalModels,
  getGrokLoginStatus,
  getProjectModels,
  getRuntime,
  getSnapshot,
  importExternalNovel,
  importSimulationProfile,
  listProjects,
  pauseProject,
  pollGrokLogin,
  renameProject,
  reviseCoCreate,
  resumeProject,
  runProjectDiagnostic,
  sendCoCreate,
  setProjectThinking,
  startProject,
  startAdaptation,
  startGrokLogin,
  steerProject,
  switchGlobalDefaultModel,
  switchProjectModel,
  testBackend,
  trashProject,
  uploadAdaptationSource,
  uploadSimulationFiles
} from './api.js';
import {
  appendCoCreateInput,
  coCreateStateFromError,
  coCreateStateFromEvent,
  coCreateStateFromResponse,
  createCoCreateState
} from './cocreate.js';
import { createWorkbenchState, eventStatus, reduceWebEvent } from './events.js';

const eventTypes = ['host_event', 'stream_delta', 'stream_clear', 'snapshot', 'cocreate_state'];

const coCreateTargetWordChoices = [
  { value: '5000', label: '5,000' },
  { value: '10000', label: '10,000' },
  { value: '30000', label: '30,000' },
  { value: 'custom', label: 'Custom' }
];

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

function createExternalImportState() {
  return {
    sourceFile: null,
    from: '',
    status: 'idle',
    events: [],
    message: '',
    error: ''
  };
}

function createExportState() {
  return {
    path: '',
    format: 'txt',
    from: '',
    to: '',
    overwrite: false,
    status: 'idle',
    result: null,
    message: '',
    error: ''
  };
}

function createDiagnosticState() {
  return {
    status: 'idle',
    report: null,
    runtime: null,
    exportPath: '',
    error: ''
  };
}

const grokOAuthDefaults = {
  provider: 'grok-oauth',
  type: 'grok',
  auth: 'grok_oauth',
  account_id: 'default',
  account_name: 'Default',
  model: 'grok-4.3-latest'
};

function createCustomModelState() {
  return {
    mode: 'existing',
    role: 'default',
    provider: '',
    preset: 'deepseek',
    type: 'openai',
    model: '',
    base_url: '',
    api_key: '',
    api: 'chat',
    account_id: grokOAuthDefaults.account_id,
    account_name: grokOAuthDefaults.account_name,
    callback_input: '',
    grok_login: null,
    grok_status: null,
    grok_message: ''
  };
}

const providerPresets = [
  { label: 'DeepSeek', provider: 'deepseek', type: 'deepseek', model: 'deepseek-chat', requiresKey: true },
  { label: 'OpenAI', provider: 'openai', type: 'openai', model: 'gpt-4.1', requiresKey: true },
  { label: 'Anthropic', provider: 'anthropic', type: 'anthropic', model: 'claude-sonnet-4-5', requiresKey: true },
  { label: 'Gemini', provider: 'gemini', type: 'gemini', model: 'gemini-2.5-pro', requiresKey: true },
  { label: 'Qwen', provider: 'qwen', type: 'qwen', model: 'qwen-max', requiresKey: true },
  { label: 'GLM', provider: 'glm', type: 'glm', model: 'glm-4.5', requiresKey: true },
  { label: 'OpenRouter', provider: 'openrouter', type: 'openrouter', model: 'openai/gpt-4.1', requiresKey: true },
  { label: 'Grok API Key', provider: 'grok', type: 'grok', model: 'grok-4.3-latest', requiresKey: true },
  { label: 'Ollama', provider: 'ollama', type: 'ollama', base_url: 'http://localhost:11434', model: 'qwen3:8b', requiresKey: false }
];

const customProviderTypes = ['openai', 'anthropic', 'gemini', 'grok'];

export default function App() {
  const [runtime, setRuntime] = useState(null);
  const [projects, setProjects] = useState([]);
  const [activeProject, setActiveProject] = useState(null);
  const [workbench, setWorkbench] = useState(createWorkbenchState);
  const [newProjectName, setNewProjectName] = useState('');
  const [projectMenu, setProjectMenu] = useState(null);
  const [renameDialog, setRenameDialog] = useState(null);
  const [deleteDialog, setDeleteDialog] = useState(null);
  const [composerText, setComposerText] = useState('');
  const [steerText, setSteerText] = useState('');
  const [sideView, setSideView] = useState('status');
  const [simulation, setSimulation] = useState(createSimulationState);
  const [adaptation, setAdaptation] = useState(createAdaptationState);
  const [externalImport, setExternalImport] = useState(createExternalImportState);
  const [exportJob, setExportJob] = useState(createExportState);
  const [diagnostic, setDiagnostic] = useState(createDiagnosticState);
  const [coCreate, setCoCreate] = useState(createCoCreateState);
  const [modelConfig, setModelConfig] = useState(null);
  const [customModel, setCustomModel] = useState(createCustomModelState);
  const [backendStatus, setBackendStatus] = useState(null);
  const [connection, setConnection] = useState('idle');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const lastSeqRef = useRef(0);

  const snapshot = workbench.snapshot;
  const quickStartAvailable = Boolean(activeProject && isFreshProject(snapshot));
  const workspaceProgress = useMemo(
    () => deriveWorkspaceProgress(snapshot, workbench.eventRows),
    [snapshot, workbench.eventRows]
  );

  const resetProjectScopedState = useCallback((clearProject = false) => {
    lastSeqRef.current = 0;
    setWorkbench(createWorkbenchState());
    setSimulation(createSimulationState());
    setAdaptation(createAdaptationState());
    setExternalImport(createExternalImportState());
    setExportJob(createExportState());
    setDiagnostic(createDiagnosticState());
    setCoCreate(createCoCreateState());
    setModelConfig(null);
    setCustomModel(createCustomModelState());
    setBackendStatus(null);
    if (clearProject) {
      setActiveProject(null);
    }
  }, []);

  const refreshProjects = useCallback(async () => {
    const data = await listProjects();
    setProjects(data.projects || []);
  }, []);

  const refreshGlobalModels = useCallback(async () => {
    const data = await getGlobalModels();
    setModelConfig(data.models || null);
    return data.models || null;
  }, []);

  const loadShell = useCallback(async () => {
    setError('');
    try {
      const [runtimeData, projectsData, modelData] = await Promise.all([getRuntime(), listProjects(), getGlobalModels()]);
      setRuntime(runtimeData);
      setProjects(projectsData.projects || []);
      setModelConfig(modelData.models || null);
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    loadShell();
  }, [loadShell]);

  useEffect(() => {
    if (!projectMenu) {
      return undefined;
    }
    const closeMenu = () => setProjectMenu(null);
    const closeOnEscape = (event) => {
      if (event.key === 'Escape') {
        closeMenu();
      }
    };
    window.addEventListener('click', closeMenu);
    window.addEventListener('keydown', closeOnEscape);
    return () => {
      window.removeEventListener('click', closeMenu);
      window.removeEventListener('keydown', closeOnEscape);
    };
  }, [projectMenu]);

  const openProject = useCallback(async (project) => {
    setBusy(true);
    setError('');
    resetProjectScopedState();
    try {
      const [snapshotData, modelData, backendData] = await Promise.all([
        getSnapshot(project.id),
        getProjectModels(project.id),
        getBackendStatus(project.id)
      ]);
      setActiveProject(snapshotData.project);
      setWorkbench({ ...createWorkbenchState(), snapshot: snapshotData.snapshot });
      setModelConfig(modelData.models || null);
      setBackendStatus(backendData.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }, [resetProjectScopedState]);

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
      if (event.seq <= lastSeqRef.current) {
        return;
      }
      setWorkbench((previous) => {
        const next = reduceWebEvent(previous, event);
        lastSeqRef.current = next.lastSeq;
        return next;
      });
      if (event.type === 'cocreate_state') {
        setCoCreate((previous) => coCreateStateFromEvent(event, previous));
      }
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

  const openProjectContextMenu = (event, project) => {
    event.preventDefault();
    event.stopPropagation();
    const menuWidth = 220;
    const menuHeight = 112;
    setProjectMenu({
      project,
      x: Math.min(event.clientX, Math.max(8, window.innerWidth - menuWidth - 8)),
      y: Math.min(event.clientY, Math.max(8, window.innerHeight - menuHeight - 8))
    });
  };

  const openProjectMoreMenu = (event, project) => {
    event.preventDefault();
    event.stopPropagation();
    const rect = event.currentTarget.getBoundingClientRect();
    const menuWidth = 220;
    setProjectMenu({
      project,
      x: Math.min(rect.left, Math.max(8, window.innerWidth - menuWidth - 8)),
      y: rect.bottom + 6
    });
  };

  const beginProjectRename = (project) => {
    setProjectMenu(null);
    setRenameDialog({ project, name: project.name || project.id });
  };

  const submitProjectRename = async (event) => {
    event.preventDefault();
    const project = renameDialog?.project;
    const name = String(renameDialog?.name || '').trim();
    if (!project || !name) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const updated = await renameProject(project.id, name);
      setProjects((previous) => previous.map((item) => (item.id === updated.id ? updated : item)));
      setActiveProject((previous) => (previous?.id === updated.id ? updated : previous));
      setRenameDialog(null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const beginProjectDelete = (project) => {
    setProjectMenu(null);
    setDeleteDialog({ project });
  };

  const confirmProjectDelete = async () => {
    const project = deleteDialog?.project;
    if (!project) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await trashProject(project.id);
      setProjects((previous) => previous.filter((item) => item.id !== project.id));
      if (activeProject?.id === project.id) {
        resetProjectScopedState(true);
        await refreshGlobalModels();
      }
      setDeleteDialog(null);
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
    await runAction((projectId) => (quickStartAvailable ? startProject(projectId, text) : continueProject(projectId, text)));
    setComposerText('');
  };

  const pauseWriting = async () => {
    await runAction((projectId) => pauseProject(projectId));
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

  const importExternalSource = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!activeProject?.id || !file) {
      return;
    }
    setBusy(true);
    setExternalImport((previous) => ({
      ...previous,
      sourceFile: null,
      status: 'running',
      events: [],
      message: '',
      error: ''
    }));
    try {
      const data = await importExternalNovel(activeProject.id, file, externalImport.from);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setExternalImport((previous) => ({
        ...previous,
        sourceFile: data.source_file || null,
        status: 'done',
        events: data.events || [],
        message: data.label ? `已导入并恢复：${data.label}` : `已导入 ${data.source_file?.name || file.name}`,
        error: ''
      }));
    } catch (err) {
      setExternalImport((previous) => ({
        ...previous,
        status: 'error',
        events: err.data?.events || previous.events,
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const runExport = async () => {
    if (!activeProject?.id) {
      return;
    }
    let from;
    let to;
    try {
      from = optionalNonNegativeInt(exportJob.from, 'from');
      to = optionalNonNegativeInt(exportJob.to, 'to');
    } catch (err) {
      setExportJob((previous) => ({ ...previous, error: err.message }));
      return;
    }
    setBusy(true);
    setExportJob((previous) => ({ ...previous, status: 'running', result: null, message: '', error: '' }));
    try {
      const data = await exportProject(activeProject.id, {
        path: exportJob.path,
        format: exportJob.format,
        from,
        to,
        overwrite: exportJob.overwrite
      });
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setExportJob((previous) => ({
        ...previous,
        status: 'done',
        result: data.export || null,
        message: data.export?.path ? `已导出 ${data.export.path}` : '导出完成',
        error: ''
      }));
    } catch (err) {
      setExportJob((previous) => ({ ...previous, status: 'error', error: err.message }));
    } finally {
      setBusy(false);
    }
  };

  const runDiagnostic = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setDiagnostic((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await runProjectDiagnostic(activeProject.id);
      setDiagnostic({
        status: 'done',
        report: data.report || null,
        runtime: data.runtime || null,
        exportPath: data.export_path || '',
        error: ''
      });
    } catch (err) {
      setDiagnostic((previous) => ({ ...previous, status: 'error', error: err.message }));
    } finally {
      setBusy(false);
    }
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

  const beginCoCreateFlow = async (kind) => {
    if (!activeProject?.id) {
      return;
    }
    const initial = coCreate.input.trim();
    if (kind === 'normal' && !initial) {
      setCoCreate((previous) => ({ ...previous, error: '先输入一个核心想法' }));
      return;
    }
    if (kind === 'adapt' && (!adaptation.sourceFile?.relative_path || adaptation.analysisStatus !== 'done')) {
      setCoCreate((previous) => ({ ...previous, error: '先完成原文分析并选择模式' }));
      return;
    }
    setBusy(true);
    setCoCreate((previous) => ({ ...previous, kind, status: 'running', error: '', startMessage: '' }));
    try {
      const payload = buildBeginCoCreatePayload({
        kind,
        initial,
        sourceFile: adaptation.sourceFile?.relative_path,
        mode: adaptation.mode,
        targetTotalWords: resolveCoCreateTargetTotalWords(coCreate)
      });
      const data = await beginCoCreate(activeProject.id, payload);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    } finally {
      setBusy(false);
    }
  };

  const submitCoCreate = async (event) => {
    event.preventDefault();
    const text = coCreate.input.trim();
    const hasBackendSession = coCreate.active || coCreate.messages.length > 0;
    if (!activeProject?.id || !text || !hasBackendSession) {
      return;
    }
    setBusy(true);
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await sendCoCreate(activeProject.id, text, 'custom');
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    } finally {
      setBusy(false);
    }
  };

  const submitCoCreateSuggestion = async (suggestion) => {
    const text = String(suggestion || '').trim();
    const hasBackendSession = coCreate.active || coCreate.messages.length > 0;
    if (!activeProject?.id || !text || !hasBackendSession || busy) {
      return;
    }
    setBusy(true);
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '', input: '' }));
    try {
      const data = await sendCoCreate(activeProject.id, text, 'suggestion');
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    } finally {
      setBusy(false);
    }
  };

  const reviseCoCreateMessage = async (messageId, text) => {
    if (!activeProject?.id || !messageId || !String(text || '').trim() || busy) {
      return;
    }
    setBusy(true);
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await reviseCoCreate(activeProject.id, messageId, text);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    } finally {
      setBusy(false);
    }
  };

  const commitCoCreateFlow = async () => {
    if (!activeProject?.id || !coCreate.ready || !coCreate.draftPrompt.trim()) {
      return;
    }
    setBusy(true);
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await commitCoCreate(activeProject.id);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    } finally {
      setBusy(false);
    }
  };

  const cancelCoCreateFlow = async () => {
    if (!activeProject?.id || !coCreate.active) {
      setCoCreate(createCoCreateState());
      return;
    }
    setBusy(true);
    try {
      const data = await cancelCoCreate(activeProject.id);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate(createCoCreateState());
    } catch (err) {
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    } finally {
      setBusy(false);
    }
  };

  const refreshBackendStatus = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await getBackendStatus(activeProject.id);
      setBackendStatus(data.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const runBackendTest = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await testBackend(activeProject.id);
      setBackendStatus(data.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const switchModelRoute = async (role, provider, model) => {
    if (!activeProject?.id || !provider || !model) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await switchProjectModel(activeProject.id, role, provider, model);
      setModelConfig(data.models || modelConfig);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      const status = await getBackendStatus(activeProject.id);
      setBackendStatus(status.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const switchDefaultModel = async (provider, model) => {
    if (!provider || !model) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await switchGlobalDefaultModel(provider, model);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (!activeProject?.id) {
        setModelConfig(data.models || null);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const changeThinking = async (role, level) => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await setProjectThinking(activeProject.id, role, level);
      setModelConfig(data.models || modelConfig);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const submitCustomModel = async (event) => {
    event.preventDefault();
    const payload = buildModelAddPayload(customModel, modelConfig);
    if (!activeProject?.id || !canSubmitModelAdd(customModel, modelConfig)) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await addProviderModel(activeProject.id, payload);
      setModelConfig(data.models || modelConfig);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCustomModel(createCustomModelState());
      const status = await getBackendStatus(activeProject.id);
      setBackendStatus(status.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const startGrokOAuthLogin = async () => {
    const authWindow = openPendingGrokAuthWindow();
    setError('');
    setCustomModel((previous) => ({ ...previous, grok_message: '正在创建 Grok OAuth 登录会话...' }));
    try {
      const data = await startGrokLogin(activeProject?.id, customModel.account_id, customModel.account_name, true);
      const login = data.login || null;
      const authorizeURL = grokAuthorizeURL(login);
      const browserOpened = Boolean(data.browser_opened || data.browserOpened);
      const browserOpenError = String(data.browser_open_error || data.browserOpenError || '').trim();
      let openedAuthorize = browserOpened;
      if (browserOpened) {
        closeGrokAuthWindow(authWindow);
      } else {
        openedAuthorize = navigateGrokAuthWindow(authWindow, authorizeURL);
      }
      setCustomModel((previous) => ({
        ...previous,
        grok_login: login,
        grok_status: grokLoggedIn(previous.grok_status) ? previous.grok_status : grokStatusFromLogin(login) || previous.grok_status,
        grok_message: grokLoginMessage(login) || grokOpenMessage(openedAuthorize, browserOpenError)
      }));
    } catch (err) {
      closeGrokAuthWindow(authWindow);
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const pollGrokOAuthLogin = async () => {
    setError('');
    try {
      const data = await pollGrokLogin(activeProject?.id);
      const login = data.login || null;
      setCustomModel((previous) => ({
        ...previous,
        grok_login: login,
        grok_status: grokLoggedIn(previous.grok_status) ? previous.grok_status : grokStatusFromLogin(login) || previous.grok_status,
        grok_message: grokLoginMessage(login) || (grokLoginDone(login) ? 'Grok OAuth 已完成' : '等待 Grok OAuth 回调')
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const completeGrokOAuthLogin = async () => {
    const callback = String(customModel.callback_input || '').trim();
    if (!callback) {
      setCustomModel((previous) => ({ ...previous, grok_message: '请粘贴 callback URL、query string 或一次性 code' }));
      return;
    }
    setError('');
    try {
      const data = await completeGrokLogin(activeProject?.id, callback);
      setCustomModel((previous) => ({
        ...previous,
        callback_input: '',
        grok_status: data.status || null,
        grok_message: grokLoggedIn(data.status) ? 'Grok OAuth 已登录' : grokAuthSummary(data.status)
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const refreshGrokOAuthStatus = async () => {
    setError('');
    try {
      const data = await getGrokLoginStatus(activeProject?.id, customModel.account_id);
      setCustomModel((previous) => ({
        ...previous,
        grok_status: data.status || null,
        grok_message: grokAuthSummary(data.status)
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
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
          <div className="brand-lockup">
            <div className="brand-mark">
              <BookOpen size={20} />
            </div>
            <div>
              <div className="eyebrow">ainovel</div>
              <h1>小说工作台</h1>
            </div>
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
              <div
                key={project.id}
                className={project.id === activeProject?.id ? 'project-row active' : 'project-row'}
                onContextMenu={(event) => openProjectContextMenu(event, project)}
              >
                <button className="project-open-button" onClick={() => openProject(project)} type="button">
                  <BookOpen size={17} />
                  <span>
                    <strong>{project.name || project.id}</strong>
                    <small>{formatDate(project.last_accessed_at || project.created_at)}</small>
                  </span>
                </button>
                <button
                  className="project-more-button"
                  onClick={(event) => openProjectMoreMenu(event, project)}
                  title="项目操作"
                  type="button"
                >
                  <MoreHorizontal size={17} />
                </button>
              </div>
            ))
          )}
        </div>

        {projectMenu ? (
          <div
            className="project-menu"
            style={{ left: projectMenu.x, top: projectMenu.y }}
            onClick={(event) => event.stopPropagation()}
          >
            <button onClick={() => beginProjectRename(projectMenu.project)} type="button">
              <Pencil size={16} />
              <span>重命名项目</span>
            </button>
            <button className="danger" onClick={() => beginProjectDelete(projectMenu.project)} type="button">
              <Trash2 size={16} />
              <span>移入回收站</span>
            </button>
          </div>
        ) : null}
      </aside>

      {renameDialog ? (
        <div className="dialog-backdrop" onMouseDown={() => setRenameDialog(null)}>
          <form className="compact-dialog" onMouseDown={(event) => event.stopPropagation()} onSubmit={submitProjectRename}>
            <div className="dialog-title">
              <Pencil size={17} />
              <strong>重命名项目</strong>
            </div>
            <input
              autoFocus
              disabled={busy}
              value={renameDialog.name}
              onChange={(event) => setRenameDialog((previous) => ({ ...previous, name: event.target.value }))}
              onKeyDown={(event) => {
                if (event.key === 'Escape') {
                  event.preventDefault();
                  setRenameDialog(null);
                }
              }}
            />
            <div className="dialog-actions">
              <button className="tool-button" disabled={busy} onClick={() => setRenameDialog(null)} type="button">
                <X size={16} />
                取消
              </button>
              <button className="tool-button accent" disabled={busy || !String(renameDialog.name || '').trim()} type="submit">
                <Check size={16} />
                保存
              </button>
            </div>
          </form>
        </div>
      ) : null}

      {deleteDialog ? (
        <div className="dialog-backdrop" onMouseDown={() => setDeleteDialog(null)}>
          <div className="compact-dialog" onMouseDown={(event) => event.stopPropagation()}>
            <div className="dialog-title danger">
              <Trash2 size={17} />
              <strong>移入回收站？</strong>
            </div>
            <p className="dialog-copy">{deleteDialog.project.name || deleteDialog.project.id}</p>
            <div className="dialog-actions">
              <button className="tool-button" disabled={busy} onClick={() => setDeleteDialog(null)} type="button">
                <X size={16} />
                取消
              </button>
              <button className="tool-button danger-action" disabled={busy} onClick={confirmProjectDelete} type="button">
                <Trash2 size={16} />
                移入回收站
              </button>
            </div>
          </div>
        </div>
      ) : null}

      <main className="writing-pane">
        <header className="workspace-toolbar">
          <div className="workspace-heading">
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
              className="tool-button"
              disabled={!activeProject || busy}
              onClick={pauseWriting}
              type="button"
            >
              <PauseCircle size={16} />
              Pause
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

        <WorkspaceProgress progress={workspaceProgress} />

        {error ? <div className="error-banner">{error}</div> : null}

        <div className="workbench-stack">
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
                <span>从左侧选择项目，或创建一本新小说开始工作。</span>
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
        </div>

        <form className="composer" onSubmit={submitContinue}>
          <input
            aria-label={quickStartAvailable ? '快速启动输入' : '继续创作输入'}
            disabled={!activeProject || busy}
            placeholder={quickStartAvailable ? '写下新书核心想法，直接启动...' : '继续、补充或要求下一步...'}
            value={composerText}
            onChange={(event) => setComposerText(event.target.value)}
          />
          <button className="tool-button accent" disabled={!activeProject || busy} type="submit">
            {quickStartAvailable ? <Play size={16} /> : <Send size={16} />}
            {quickStartAvailable ? 'Start' : 'Continue'}
          </button>
        </form>
      </main>

      <aside className="status-pane">
        <div className="side-tabs">
          <button className={sideView === 'status' ? 'active' : ''} onClick={() => setSideView('status')} title="状态" type="button">
            <CircleDot size={16} />
            状态
          </button>
          <button className={sideView === 'cocreate' ? 'active' : ''} onClick={() => setSideView('cocreate')} title="共创" type="button">
            <MessageSquareText size={16} />
            共创
          </button>
          <button className={sideView === 'simulate' ? 'active' : ''} onClick={() => setSideView('simulate')} title="画像" type="button">
            <WandSparkles size={16} />
            画像
          </button>
          <button className={sideView === 'adapt' ? 'active' : ''} onClick={() => setSideView('adapt')} title="改编" type="button">
            <FileText size={16} />
            改编
          </button>
          <button className={sideView === 'import' ? 'active' : ''} onClick={() => setSideView('import')} title="导入" type="button">
            <Upload size={16} />
            导入
          </button>
          <button className={sideView === 'export' ? 'active' : ''} onClick={() => setSideView('export')} title="导出" type="button">
            <Download size={16} />
            导出
          </button>
          <button className={sideView === 'diag' ? 'active' : ''} onClick={() => setSideView('diag')} title="诊断" type="button">
            <Activity size={16} />
            诊断
          </button>
          <button className={sideView === 'cache' ? 'active' : ''} onClick={() => setSideView('cache')} title="缓存" type="button">
            <Database size={16} />
            缓存
          </button>
          <button className={sideView === 'backend' ? 'active' : ''} onClick={() => setSideView('backend')} title="后端" type="button">
            <Server size={16} />
            后端
          </button>
          <button className={sideView === 'models' ? 'active' : ''} onClick={() => setSideView('models')} title="模型" type="button">
            <Settings size={16} />
            模型
          </button>
        </div>

        <div className="side-panel">
          {sideView === 'status' ? (
            <StatusPanel snapshot={snapshot} activeProject={activeProject} onSteer={submitSteer} steerText={steerText} setSteerText={setSteerText} busy={busy} />
          ) : sideView === 'cocreate' ? (
            <CoCreatePanel
              activeProject={activeProject}
              busy={busy}
              coCreate={coCreate}
              setCoCreate={setCoCreate}
              adaptation={adaptation}
              onBegin={beginCoCreateFlow}
              onSubmit={submitCoCreate}
              onSuggestion={submitCoCreateSuggestion}
              onRevise={reviseCoCreateMessage}
              onCommit={commitCoCreateFlow}
              onCancel={cancelCoCreateFlow}
            />
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
              onCoCreate={() => {
                setSideView('cocreate');
                beginCoCreateFlow('adapt');
              }}
            />
          ) : sideView === 'import' ? (
            <ImportPanel
              activeProject={activeProject}
              busy={busy}
              externalImport={externalImport}
              setExternalImport={setExternalImport}
              onImport={importExternalSource}
            />
          ) : sideView === 'export' ? (
            <ExportPanel
              activeProject={activeProject}
              busy={busy}
              exportJob={exportJob}
              setExportJob={setExportJob}
              onExport={runExport}
            />
          ) : sideView === 'diag' ? (
            <DiagnosticPanel
              activeProject={activeProject}
              busy={busy}
              diagnostic={diagnostic}
              onRun={runDiagnostic}
            />
          ) : sideView === 'cache' ? (
            <CachePanel snapshot={snapshot} />
          ) : sideView === 'backend' ? (
            <BackendPanel backend={backendStatus} busy={busy} onRefresh={refreshBackendStatus} onTest={runBackendTest} />
          ) : (
            <ModelPanel
              activeProject={activeProject}
              runtime={runtime}
              modelConfig={modelConfig}
              customModel={customModel}
              setCustomModel={setCustomModel}
              busy={busy}
              onSwitchDefault={switchDefaultModel}
              onSwitch={switchModelRoute}
              onThinking={changeThinking}
              onAddCustom={submitCustomModel}
              onStartGrokLogin={startGrokOAuthLogin}
              onPollGrokLogin={pollGrokOAuthLogin}
              onCompleteGrokLogin={completeGrokOAuthLogin}
              onRefreshGrokStatus={refreshGrokOAuthStatus}
            />
          )}
        </div>
      </aside>
    </div>
  );
}

function WorkspaceProgress({ progress }) {
  const chapterPercent = progress.totalChapters > 0
    ? Math.min(100, Math.round((progress.completedChapters / progress.totalChapters) * 100))
    : 0;
  return (
    <section className="workspace-progress" aria-label="Workspace progress">
      <div className="workspace-progress-meter" aria-hidden="true">
        <span style={{ width: `${chapterPercent}%` }} />
      </div>
      <div className="workspace-progress-items">
        <ProgressItem label="Status" value={progress.statusLabel} />
        <ProgressItem label="Chapters" value={progress.chapterLabel} />
        <ProgressItem label="Current" value={progress.currentChapterLabel} />
        <ProgressItem label="Words" value={progress.wordLabel} />
        <ProgressItem label="Running" value={progress.runningLabel} wide />
      </div>
    </section>
  );
}

function ProgressItem({ label, value, wide = false }) {
  return (
    <div className={`workspace-progress-item ${wide ? 'wide' : ''}`}>
      <span>{label}</span>
      <strong title={value}>{value}</strong>
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

function CoCreatePanel({
  activeProject,
  busy,
  coCreate,
  setCoCreate,
  adaptation,
  onBegin,
  onSubmit,
  onSuggestion,
  onRevise,
  onCommit,
  onCancel
}) {
  const [editing, setEditing] = useState(null);
  const hasConversation = coCreate.messages.length > 0;
  const hasBackendSession = coCreate.active || hasConversation;
  const showTargetControls = !hasBackendSession && coCreate.kind !== 'adapt';
  const targetTotalWords = resolveCoCreateTargetTotalWords(coCreate);
  const canBeginNormal = Boolean(activeProject && !busy && !hasBackendSession && coCreate.input.trim() && targetTotalWords > 0);
  const canBeginStage = Boolean(activeProject && !busy && !hasBackendSession);
  const canBeginAdapt = Boolean(
    activeProject &&
      !busy &&
      !hasBackendSession &&
      adaptation.sourceFile?.relative_path &&
      adaptation.analysisStatus === 'done'
  );
  const canSend = Boolean(activeProject && !busy && hasBackendSession && coCreate.input.trim());
  const canCommit = Boolean(activeProject && !busy && coCreate.ready && coCreate.draftPrompt.trim());
  const canCancel = Boolean(activeProject && !busy && hasBackendSession);
  const title = coCreateTitle(coCreate.kind);
  return (
    <div className="side-content cocreate-panel">
      {coCreate.error ? <div className="error-banner compact">{coCreate.error}</div> : null}

      <section className="cocreate-section">
        <div className="section-title">
          <MessageSquareText size={17} />
          <span>{title}</span>
        </div>
        <div className="cocreate-mode-grid">
          <button className="tool-button" disabled={!canBeginNormal} onClick={() => onBegin('normal')} type="button">
            <SquarePen size={16} />
            普通
          </button>
          <button className="tool-button" disabled={!canBeginStage} onClick={() => onBegin('stage')} type="button">
            <PauseCircle size={16} />
            Stage
          </button>
          <button className="tool-button" disabled={!canBeginAdapt} onClick={() => onBegin('adapt')} type="button">
            <FileText size={16} />
            Adapt
          </button>
        </div>
        {coCreate.modeLocked ? (
          <div className="success-note">已锁定 {coCreate.adaptMode} / {coCreate.rewritePolicy}</div>
        ) : null}
      </section>

      <section className="cocreate-section cocreate-dialog">
        <div className="cocreate-messages">
          {coCreate.messages.length === 0 ? (
            <div className="empty-state">暂无共创对话</div>
          ) : (
            coCreate.messages.map((message, index) => (
              <div className={`cocreate-message ${message.role}`} key={message.id || `${message.role}-${index}`}>
                <div className="cocreate-message-head">
                  <strong>{coCreateRoleLabel(message.role)}</strong>
                  {message.source ? <span>{message.source === 'suggestion' ? '选项' : '补充'}</span> : null}
                  {message.editable ? (
                    <button
                      className="icon-button inline"
                      disabled={busy}
                      onClick={() => setEditing({ id: message.id, text: message.content })}
                      title="修改这次选择"
                      type="button"
                    >
                      <Pencil size={14} />
                    </button>
                  ) : null}
                </div>
                {editing?.id === message.id ? (
                  <form
                    className="cocreate-edit-form"
                    onSubmit={async (event) => {
                      event.preventDefault();
                      const text = editing.text.trim();
                      if (!text) {
                        return;
                      }
                      await onRevise(message.id, text);
                      setEditing(null);
                    }}
                  >
                    <textarea
                      autoFocus
                      disabled={busy}
                      value={editing.text}
                      onChange={(event) => setEditing((previous) => ({ ...previous, text: event.target.value }))}
                      onKeyDown={(event) => {
                        if (event.key === 'Escape') {
                          event.preventDefault();
                          setEditing(null);
                        }
                      }}
                    />
                    <div className="inline-actions">
                      <button className="icon-button" disabled={busy} onClick={() => setEditing(null)} title="取消" type="button">
                        <X size={15} />
                      </button>
                      <button className="icon-button primary" disabled={busy || !editing.text.trim()} title="保存并重跑" type="submit">
                        <Check size={15} />
                      </button>
                    </div>
                  </form>
                ) : (
                  <p>{message.content}</p>
                )}
              </div>
            ))
          )}
        </div>
        {coCreate.streamThinking || coCreate.streamReply ? (
          <div className="cocreate-progress">
            {coCreate.streamThinking ? (
              <div className="cocreate-preview thinking">
                <strong>Thinking</strong>
                <p>{coCreate.streamThinking}</p>
              </div>
            ) : null}
            {coCreate.streamReply ? (
              <div className="cocreate-preview reply">
                <strong>AI</strong>
                <p>{coCreate.streamReply}</p>
              </div>
            ) : null}
          </div>
        ) : null}
      </section>

      {coCreate.suggestions.length ? (
        <section className="cocreate-section">
          <div className="suggestion-list">
            {coCreate.suggestions.slice(0, 3).map((suggestion) => (
              <button
                className="suggestion-option"
                disabled={busy}
                key={suggestion}
                onClick={() => onSuggestion(suggestion)}
                type="button"
              >
                <Send size={15} />
                {suggestion}
              </button>
            ))}
          </div>
        </section>
      ) : null}

      <div className="cocreate-sticky-workspace">
        <form className="cocreate-form" onSubmit={hasBackendSession ? onSubmit : (event) => {
          event.preventDefault();
          onBegin('normal');
        }}>
        <textarea
          aria-label="共创输入"
          disabled={!activeProject || busy}
          placeholder={hasBackendSession ? '继续补充你的想法...' : '输入你的核心想法，或先进入 Stage/Adapt 共创'}
          value={coCreate.input}
          onChange={(event) => setCoCreate((previous) => appendCoCreateInput(previous, event.target.value))}
        />
        {showTargetControls ? (
          <div className="cocreate-target">
            <div className="cocreate-target-options" aria-label="Target total words">
              {coCreateTargetWordChoices.map((choice) => (
                <label
                  className={`target-option ${(coCreate.targetTotalWordsChoice || '5000') === choice.value ? 'active' : ''}`}
                  key={choice.value}
                >
                  <input
                    checked={(coCreate.targetTotalWordsChoice || '5000') === choice.value}
                    disabled={!activeProject || busy}
                    name="cocreate-target-total-words"
                    type="radio"
                    value={choice.value}
                    onChange={(event) => {
                      const value = event.target.value;
                      setCoCreate((previous) => ({
                        ...previous,
                        targetTotalWordsChoice: value,
                        customTargetTotalWords: value === 'custom' ? previous.customTargetTotalWords || '' : previous.customTargetTotalWords
                      }));
                    }}
                  />
                  <span>{choice.label}</span>
                </label>
              ))}
            </div>
            {(coCreate.targetTotalWordsChoice || '5000') === 'custom' ? (
              <label className="field-label">
                <span>Target words</span>
                <input
                  disabled={!activeProject || busy}
                  inputMode="numeric"
                  min="1"
                  placeholder="12000"
                  type="number"
                  value={coCreate.customTargetTotalWords || ''}
                  onChange={(event) => setCoCreate((previous) => ({ ...previous, customTargetTotalWords: event.target.value }))}
                />
              </label>
            ) : null}
          </div>
        ) : null}
        <button className="tool-button accent full-width" disabled={hasBackendSession ? !canSend : !canBeginNormal} type="submit">
          <Send size={16} />
          {hasBackendSession ? '发送' : '开始普通共创'}
        </button>
        </form>

        <section className="cocreate-section">
        <div className={`workflow-status ${coCreate.status}`}>
          <strong>{coCreateStatusText(coCreate.status, coCreate.ready)}</strong>
          <span>{coCreate.startMessage || (coCreate.ready ? 'draft prompt 已就绪' : '等待共创完成')}</span>
        </div>
        <div className="draft-preview">
          {coCreate.draftPrompt ? <pre>{coCreate.draftPrompt}</pre> : <span className="muted">AI 会在这里整理 draft prompt</span>}
        </div>
        <div className="cocreate-actions">
          <button className="tool-button accent" disabled={!canCommit} onClick={onCommit} type="button">
            <Play size={16} />
            启动
          </button>
          <button className="tool-button" disabled={!canCancel} onClick={onCancel} type="button">
            <ListRestart size={16} />
            取消
          </button>
        </div>
        </section>
      </div>
    </div>
  );
}

function AdaptationPanel({ activeProject, busy, adaptation, setAdaptation, onUploadSource, onAnalyze, onStart, onCoCreate }) {
  const latestAnalysis = latestSimulationEvent(adaptation.analysisEvents);
  const analyzed = adaptation.analysisStatus === 'done';
  const canAnalyze = Boolean(activeProject && adaptation.sourceFile && !busy && adaptation.analysisStatus !== 'running');
  const canCoCreate = Boolean(activeProject && adaptation.sourceFile?.relative_path && analyzed && !busy);
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
          <button className="tool-button full-width" disabled={!canCoCreate} onClick={onCoCreate} type="button">
            <MessageSquareText size={16} />
            进入共创
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

function ImportPanel({ activeProject, busy, externalImport, setExternalImport, onImport }) {
  const latest = latestSimulationEvent(externalImport.events);
  return (
    <div className="side-content">
      {externalImport.error ? <div className="error-banner compact">{externalImport.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Upload size={17} />
          <span>外部小说导入</span>
        </div>
        <div className="tool-form">
          <label className="field-label">
            <span>续写起点</span>
            <input
              disabled={busy}
              inputMode="numeric"
              min="0"
              placeholder="0"
              type="number"
              value={externalImport.from}
              onChange={(event) => setExternalImport((previous) => ({ ...previous, from: event.target.value }))}
            />
          </label>
          <label className={`tool-button file-picker full ${!activeProject || busy ? 'disabled' : ''}`}>
            <Upload size={16} />
            上传并导入
            <input
              accept=".txt,.md,.markdown,text/plain,text/markdown"
              disabled={!activeProject || busy}
              onChange={onImport}
              type="file"
            />
          </label>
        </div>
        {externalImport.message ? <div className="success-note">{externalImport.message}</div> : null}
        <div className={`workflow-status ${externalImport.status}`}>
          <strong>{workflowStatusText(externalImport.status)}</strong>
          <span>{latest?.message || '等待导入文本'}</span>
        </div>
        {externalImport.sourceFile ? (
          <div className="file-list">
            <div className="file-row">
              <span>{externalImport.sourceFile.name}</span>
              <strong>{formatBytes(externalImport.sourceFile.size)}</strong>
            </div>
          </div>
        ) : null}
        <SimulationEventList events={externalImport.events} />
      </section>
    </div>
  );
}

function ExportPanel({ activeProject, busy, exportJob, setExportJob, onExport }) {
  const result = exportJob.result;
  return (
    <div className="side-content">
      {exportJob.error ? <div className="error-banner compact">{exportJob.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Download size={17} />
          <span>小说导出</span>
        </div>
        <div className="tool-form">
          <label className="field-label">
            <span>文件名</span>
            <input
              disabled={busy}
              placeholder="留空使用默认文件名"
              value={exportJob.path}
              onChange={(event) => setExportJob((previous) => ({ ...previous, path: event.target.value }))}
            />
          </label>
          <label className="field-label">
            <span>格式</span>
            <select
              disabled={busy}
              value={exportJob.format}
              onChange={(event) => setExportJob((previous) => ({ ...previous, format: event.target.value }))}
            >
              <option value="txt">txt</option>
              <option value="epub">epub</option>
            </select>
          </label>
          <div className="field-grid">
            <label className="field-label">
              <span>from</span>
              <input
                disabled={busy}
                inputMode="numeric"
                min="0"
                placeholder="0"
                type="number"
                value={exportJob.from}
                onChange={(event) => setExportJob((previous) => ({ ...previous, from: event.target.value }))}
              />
            </label>
            <label className="field-label">
              <span>to</span>
              <input
                disabled={busy}
                inputMode="numeric"
                min="0"
                placeholder="0"
                type="number"
                value={exportJob.to}
                onChange={(event) => setExportJob((previous) => ({ ...previous, to: event.target.value }))}
              />
            </label>
          </div>
          <label className="checkbox-row">
            <input
              checked={exportJob.overwrite}
              disabled={busy}
              type="checkbox"
              onChange={(event) => setExportJob((previous) => ({ ...previous, overwrite: event.target.checked }))}
            />
            <span>覆盖已有文件</span>
          </label>
          <button className="tool-button accent full-width" disabled={!activeProject || busy} onClick={onExport} type="button">
            <Download size={16} />
            Export
          </button>
        </div>
        {exportJob.message ? <div className="success-note">{exportJob.message}</div> : null}
        <div className={`workflow-status ${exportJob.status}`}>
          <strong>{workflowStatusText(exportJob.status)}</strong>
          <span>{result?.path || '等待导出'}</span>
        </div>
        {result ? (
          <section className="metric-grid">
            <Metric label="Chapters" value={result.chapters || 0} />
            <Metric label="Bytes" value={formatBytes(result.bytes || 0)} />
          </section>
        ) : null}
        {result?.skipped?.length ? <div className="success-note">跳过章节：{result.skipped.join(', ')}</div> : null}
      </section>
    </div>
  );
}

function DiagnosticPanel({ activeProject, busy, diagnostic, onRun }) {
  const report = diagnostic.report || {};
  const runtime = diagnostic.runtime || {};
  const stats = report.Stats || report.stats || {};
  const findings = report.Findings || report.findings || [];
  const actions = report.Actions || report.actions || [];
  const repeats = runtime.Repeats || runtime.repeats || [];
  return (
    <div className="side-content">
      {diagnostic.error ? <div className="error-banner compact">{diagnostic.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Activity size={17} />
          <span>诊断</span>
        </div>
        <button className="tool-button accent full-width" disabled={!activeProject || busy} onClick={onRun} type="button">
          <Activity size={16} />
          Run diag
        </button>
        <div className={`workflow-status ${diagnostic.status}`}>
          <strong>{workflowStatusText(diagnostic.status)}</strong>
          <span>{diagnostic.exportPath || '等待诊断'}</span>
        </div>
      </section>

      <section className="metric-grid">
        <Metric label="章节" value={`${stats.CompletedChapters || stats.completed_chapters || 0}/${stats.TotalChapters || stats.total_chapters || 0}`} />
        <Metric label="字数" value={stats.TotalWords || stats.total_words || 0} />
        <Metric label="评分" value={formatScore(stats.AvgReviewScore || stats.avg_review_score || 0)} />
        <Metric label="Actions" value={actions.length} />
      </section>

      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>发现</span>
        </div>
        <div className="finding-list">
          {findings.length === 0 ? (
            <div className="empty-state">暂无诊断发现</div>
          ) : (
            findings.slice(0, 12).map((finding, index) => (
              <div className={`finding-row ${String(finding.Severity || finding.severity || '').toLowerCase()}`} key={`${finding.Rule || finding.rule}-${index}`}>
                <strong>{finding.Title || finding.title || 'Untitled'}</strong>
                <span>{finding.Category || finding.category || '-'} · {finding.Severity || finding.severity || '-'}</span>
                <small>{finding.Suggestion || finding.suggestion || finding.Evidence || finding.evidence || '-'}</small>
              </div>
            ))
          )}
        </div>
      </section>

      <section>
        <div className="section-title">
          <ListRestart size={17} />
          <span>运行信号</span>
        </div>
        <div className="agent-list">
          <div className="backend-row">
            <strong>{runtime.CurrentStep || runtime.current_step || 'no checkpoint'}</strong>
            <span>{runtime.StuckStep || runtime.stuck_step ? `stuck ×${runtime.StuckCount || runtime.stuck_count || 0}` : 'no stuck signal'}</span>
            <small>log errors ×{runtime.LogErrors || runtime.log_errors || 0} · warn ×{runtime.LogWarns || runtime.log_warns || 0}</small>
          </div>
          {repeats.slice(0, 4).map((repeat, index) => (
            <div className="backend-row" key={`${repeat.Sig || repeat.sig}-${index}`}>
              <strong>{repeat.Sig || repeat.sig}</strong>
              <span>×{repeat.Count || repeat.count || 0}</span>
              <small>近端重复信号</small>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

function CachePanel({ snapshot }) {
  const roles = snapshot?.CachePerAgent || snapshot?.cache_per_agent || [];
  const models = snapshot?.CachePerModel || snapshot?.cache_per_model || [];
  const input = snapshot?.TotalInputTokens || 0;
  const cacheRead = snapshot?.TotalCacheReadTokens || 0;
  return (
    <div className="side-content">
      <section className="metric-grid">
        <Metric label="Input" value={formatCompact(input)} />
        <Metric label="Cache read" value={formatCompact(cacheRead)} />
        <Metric label="Output" value={formatCompact(snapshot?.TotalOutputTokens || 0)} />
        <Metric label="Cost" value={formatUSD(snapshot?.TotalCostUSD || 0)} />
      </section>
      <section className="model-summary">
        <Metric label="Cache hit" value={formatPercent(cacheRead, input)} />
        <Metric label="Saved" value={formatUSD(snapshot?.TotalSavedUSD || 0)} />
        <Metric label="Recent hit" value={formatPercent(snapshot?.OverallRecentCacheRead || 0, snapshot?.OverallRecentInput || 0)} />
        <Metric label="Usage gaps" value={snapshot?.MissingAssistantUsage || 0} />
      </section>
      <UsageList title="按角色" items={roles} labelKey="Role" />
      <UsageList title="按模型" items={models} labelKey="Model" />
    </div>
  );
}

function UsageList({ title, items, labelKey }) {
  return (
    <section>
      <div className="section-title">
        <Database size={17} />
        <span>{title}</span>
      </div>
      <div className="agent-list">
        {items.length === 0 ? (
          <div className="empty-state">暂无 usage 数据</div>
        ) : (
          items.map((item) => {
            const label = item[labelKey] || item[labelKey.toLowerCase()] || 'unknown';
            const input = item.Input || item.input || 0;
            const cacheRead = item.CacheRead || item.cache_read || 0;
            return (
              <div className="usage-row" key={label}>
                <strong>{label}</strong>
                <span>{formatPercent(cacheRead, input)}</span>
                <small>{formatCompact(input)} in / {formatCompact(item.Output || item.output || 0)} out</small>
                <small>{formatUSD(item.Cost || item.cost || 0)}</small>
              </div>
            );
          })
        )}
      </div>
    </section>
  );
}

function BackendPanel({ backend, busy, onRefresh, onTest }) {
  const calls = backend?.recent_calls || [];
  return (
    <div className="side-content">
      <section className="model-summary">
        <Metric label="Status" value={backend?.status || 'unknown'} />
        <Metric label="Provider" value={backend?.provider || '-'} />
        <Metric label="Model" value={backend?.model || '-'} />
        <Metric label="Runtime" value={backend?.runtime_state || '-'} />
      </section>
      <div className="simulation-actions">
        <button className="tool-button" disabled={busy} onClick={onRefresh} type="button">
          <RefreshCw size={16} />
          Refresh
        </button>
        <button className="tool-button accent" disabled={busy} onClick={onTest} type="button">
          <TestTube2 size={16} />
          Test
        </button>
      </div>
      {backend?.manual_test ? (
        <div className="success-note">{backend.manual_test.message}</div>
      ) : null}
      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>最近调用</span>
        </div>
        <div className="agent-list">
          {calls.length === 0 ? (
            <div className="empty-state">暂无已完成调用</div>
          ) : (
            calls.map((call, index) => (
              <div className={`backend-row ${call.failed ? 'error' : call.running ? 'running' : ''}`} key={`${call.time}-${index}`}>
                <strong>{call.category || 'CALL'} / {call.agent || 'host'}</strong>
                <span>{call.running ? 'running' : call.failed ? 'failed' : 'ok'} · {call.duration_ms || 0}ms</span>
                <small>{call.summary || '-'}</small>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

function ModelPanel({
  activeProject,
  runtime,
  modelConfig,
  customModel,
  setCustomModel,
  busy,
  onSwitchDefault,
  onSwitch,
  onThinking,
  onAddCustom,
  onStartGrokLogin,
  onPollGrokLogin,
  onCompleteGrokLogin,
  onRefreshGrokStatus
}) {
  const config = runtime?.config || {};
  const roles = modelConfig?.roles || [];
  const projectRoles = activeProject?.id ? roles : [];
  const providers = modelConfig?.providers || [];
  const levels = modelConfig?.thinking_levels || ['', 'off', 'low', 'medium', 'high', 'xhigh', 'max'];
  const providerMap = new Map(providers.map((provider) => [provider.name, provider.models || []]));
  const defaultProvider = config.provider || providers[0]?.name || '';
  const defaultModels = modelOptionsForProvider(providers, defaultProvider, config.model);
  const defaultModel = config.model || defaultModels[0] || '';
  const selectedPreset = providerPresets.find((preset) => preset.provider === customModel.preset) || providerPresets[0];
  const grokURL = grokAuthorizeURL(customModel.grok_login);
  const grokReady = grokLoggedIn(customModel.grok_status);
  const canAdd = canSubmitModelAdd(customModel, modelConfig);
  return (
    <div className="side-content">
      <section>
        <div className="section-title">
          <Settings size={17} />
          <span>当前默认模型</span>
        </div>
        <div className="default-model-controls">
          <select
            disabled={busy || providers.length === 0}
            value={defaultProvider}
            onChange={(event) => {
              const provider = event.target.value;
              const models = modelOptionsForProvider(providers, provider, '');
              onSwitchDefault(provider, models[0] || defaultModel);
            }}
          >
            {providers.length === 0 ? <option value="">无 provider</option> : null}
            {providers.map((provider) => (
              <option key={provider.name} value={provider.name}>{provider.name}</option>
            ))}
          </select>
          <select
            disabled={busy || !defaultProvider || defaultModels.length === 0}
            value={defaultModel}
            onChange={(event) => onSwitchDefault(defaultProvider, event.target.value)}
          >
            {defaultModels.length === 0 ? <option value="">无 model</option> : null}
            {defaultModels.map((model) => (
              <option key={model} value={model}>{model}</option>
            ))}
          </select>
        </div>
      </section>
      <section className="model-summary">
        <Metric label="Style" value={config.style || 'default'} />
        <Metric label="Runtime" value={runtime?.runtime_root || '-'} />
      </section>
      <section>
        <div className="section-title">
          <SlidersHorizontal size={17} />
          <span>项目模型</span>
        </div>
        <div className="model-route-list">
          {projectRoles.length === 0 ? (
            <div className="empty-state">打开项目后可配置模型</div>
          ) : (
            projectRoles.map((route) => (
              <div className="model-route" key={route.role}>
                <strong>{route.role}</strong>
                <select
                  disabled={busy}
                  value={route.provider}
                  onChange={(event) => {
                    const provider = event.target.value;
                    const models = providerMap.get(provider) || [];
                    onSwitch(route.role, provider, models[0] || route.model);
                  }}
                >
                  {providers.map((provider) => (
                    <option key={provider.name} value={provider.name}>{provider.name}</option>
                  ))}
                </select>
                <select
                  disabled={busy}
                  value={route.model}
                  onChange={(event) => onSwitch(route.role, route.provider, event.target.value)}
                >
                  {(providerMap.get(route.provider) || [route.model]).map((model) => (
                    <option key={model} value={model}>{model}</option>
                  ))}
                </select>
                <select
                  disabled={busy}
                  value={route.reasoning_effort || ''}
                  onChange={(event) => onThinking(route.role, event.target.value)}
                >
                  {levels.map((level) => (
                    <option key={level || 'inherit'} value={level}>{level || 'inherit'}</option>
                  ))}
                </select>
                <span>{route.explicit ? 'project' : 'global fallback'}</span>
              </div>
            ))
          )}
        </div>
      </section>
      <form className="custom-model-form" onSubmit={onAddCustom}>
        <div className="section-title">
          <Settings size={17} />
          <span>添加模型</span>
        </div>
        <select
          disabled={busy}
          value={customModel.role}
          onChange={(event) => setCustomModel((previous) => ({ ...previous, role: event.target.value }))}
        >
          {(roles.length ? roles : [{ role: 'default' }]).map((route) => (
            <option key={route.role} value={route.role}>{route.role}</option>
          ))}
        </select>
        <select
          disabled={busy}
          value={customModel.mode}
          onChange={(event) => setCustomModel((previous) => modelAddModeDefaults({ ...previous, mode: event.target.value }, providers))}
        >
          <option value="existing">已有 provider</option>
          <option value="preset">内置 provider</option>
          <option value="custom">Custom Proxy</option>
          <option value="grok_oauth">Grok 登录</option>
        </select>
        {customModel.mode === 'existing' ? (
          <select
            disabled={busy || providers.length === 0}
            value={customModel.provider || providers[0]?.name || ''}
            onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
          >
            {providers.length === 0 ? <option value="">无 provider</option> : null}
            {providers.map((provider) => (
              <option key={provider.name} value={provider.name}>{provider.name}</option>
            ))}
          </select>
        ) : null}
        {customModel.mode === 'preset' ? (
          <>
            <select
              disabled={busy}
              value={customModel.preset}
              onChange={(event) => setCustomModel((previous) => modelAddPresetDefaults({ ...previous, preset: event.target.value }))}
            >
              {providerPresets.map((preset) => (
                <option key={preset.provider} value={preset.provider}>{preset.label}</option>
              ))}
            </select>
            <input
              disabled={busy}
              placeholder="provider key"
              value={customModel.provider || selectedPreset.provider}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder={selectedPreset.requiresKey ? 'API key' : 'API key 可空'}
              type="password"
              value={customModel.api_key}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, api_key: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder="base URL"
              value={customModel.base_url}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, base_url: event.target.value }))}
            />
          </>
        ) : null}
        {customModel.mode === 'custom' ? (
          <>
            <input
              disabled={busy}
              placeholder="provider key"
              value={customModel.provider}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
            />
            <select
              disabled={busy}
              value={customModel.type}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, type: event.target.value }))}
            >
              {customProviderTypes.map((type) => (
                <option key={type} value={type}>{type}</option>
              ))}
            </select>
            {customModel.type === 'openai' ? (
              <select
                disabled={busy}
                value={customModel.api}
                onChange={(event) => setCustomModel((previous) => ({ ...previous, api: event.target.value }))}
              >
                <option value="chat">chat</option>
                <option value="responses">responses</option>
              </select>
            ) : null}
            <input
              disabled={busy}
              placeholder="API key 可空"
              type="password"
              value={customModel.api_key}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, api_key: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder="base URL"
              value={customModel.base_url}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, base_url: event.target.value }))}
            />
          </>
        ) : null}
        {customModel.mode === 'grok_oauth' ? (
          <>
            <input
              disabled={busy}
              placeholder="provider key"
              value={customModel.provider}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder="account ID"
              value={customModel.account_id}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, account_id: event.target.value, grok_status: null }))}
            />
            <input
              disabled={busy}
              placeholder="account name"
              value={customModel.account_name}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, account_name: event.target.value }))}
            />
            <div className="grok-action-grid">
              <button className="tool-button" onClick={onStartGrokLogin} type="button">
                <Play size={16} />
                Login
              </button>
              <button className="tool-button" onClick={onPollGrokLogin} type="button">
                <CircleDot size={16} />
                Poll
              </button>
              <button className="tool-button" onClick={onRefreshGrokStatus} type="button">
                <RefreshCw size={16} />
                Status
              </button>
              <button
                className="tool-button"
                disabled={!String(customModel.callback_input || '').trim()}
                onClick={onCompleteGrokLogin}
                type="button"
              >
                <Send size={16} />
                Complete
              </button>
            </div>
            {grokURL ? (
              <a className="grok-login-link" href={grokURL} rel="noreferrer" target="_blank">
                打开 Grok 授权页面
              </a>
            ) : null}
            <textarea
              disabled={busy}
              placeholder="callback URL / query string / one-time code"
              value={customModel.callback_input}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, callback_input: event.target.value }))}
            />
            <div className={`grok-status ${grokReady ? 'ready' : ''}`}>
              <strong>{grokReady ? 'Grok 已登录' : 'Grok 未登录'}</strong>
              <span>{customModel.grok_message || grokAuthSummary(customModel.grok_status)}</span>
            </div>
          </>
        ) : null}
        <input
          disabled={busy}
          placeholder="model"
          value={customModel.model || (customModel.mode === 'preset' ? selectedPreset.model : customModel.mode === 'grok_oauth' ? grokOAuthDefaults.model : '')}
          onChange={(event) => setCustomModel((previous) => ({ ...previous, model: event.target.value }))}
        />
        <button className="tool-button accent full-width" disabled={busy || !activeProject?.id || !canAdd} type="submit">
          <Plus size={16} />
          Add and use
        </button>
      </form>
      {modelConfig?.thinking_rule ? <div className="success-note">{modelConfig.thinking_rule}</div> : null}
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

function coCreateTitle(kind) {
  switch (kind) {
    case 'stage':
      return '阶段共创';
    case 'adapt':
      return '改编共创';
    default:
      return '普通共创';
  }
}

function coCreateRoleLabel(role) {
  switch (role) {
    case 'assistant':
      return 'AI';
    case 'system':
      return '系统';
    default:
      return '你';
  }
}

function coCreateStatusText(status, ready) {
  if (status === 'running') {
    return '进行中';
  }
  if (status === 'error') {
    return '出错';
  }
  if (status === 'started') {
    return '已启动';
  }
  return ready ? '已就绪' : '待处理';
}

function adaptationModeLabel(mode) {
  return adaptationModes.find((item) => item.value === mode)?.label || mode || '-';
}

function grokStatusFromLogin(login) {
  return login?.status || login?.Status || null;
}

function grokAuthorizeURL(login) {
  return String(login?.authorize_url || login?.AuthorizeURL || '').trim();
}

function openPendingGrokAuthWindow() {
  if (typeof window === 'undefined') {
    return null;
  }
  try {
    const authWindow = window.open('about:blank', '_blank');
    if (!authWindow) {
      return null;
    }
    authWindow.document.title = 'Grok OAuth';
    authWindow.document.body.innerHTML = '<main style="font-family: system-ui, sans-serif; padding: 24px; line-height: 1.5;"><strong>正在打开 Grok 授权页面...</strong><p>如果这个页面没有自动跳转，请回到 AINovel 点击授权链接。</p></main>';
    return authWindow;
  } catch {
    return null;
  }
}

function closeGrokAuthWindow(authWindow) {
  if (!authWindow) {
    return;
  }
  try {
    authWindow.close();
  } catch {
    // Ignore browser popup cleanup failures.
  }
}

function navigateGrokAuthWindow(authWindow, authorizeURL) {
  const url = String(authorizeURL || '').trim();
  if (!url) {
    closeGrokAuthWindow(authWindow);
    return false;
  }
  if (authWindow) {
    try {
      authWindow.opener = null;
      authWindow.location.replace(url);
      return true;
    } catch {
      closeGrokAuthWindow(authWindow);
    }
  }
  if (typeof window === 'undefined') {
    return false;
  }
  try {
    return Boolean(window.open(url, '_blank', 'noopener,noreferrer'));
  } catch {
    return false;
  }
}

function grokOpenMessage(openedAuthorize, browserOpenError) {
  if (openedAuthorize) {
    return '已打开 Grok 授权页面';
  }
  if (browserOpenError) {
    return `授权链接已生成；后端打开浏览器失败：${browserOpenError}`;
  }
  return '授权链接已生成，请点击打开 Grok 授权页面';
}

function grokLoginDone(login) {
  return Boolean(login?.done || login?.Done);
}

function grokLoginMessage(login) {
  const status = grokStatusFromLogin(login);
  return String(login?.message || login?.Message || status?.message || status?.Message || '').trim();
}

function grokLoggedIn(status) {
  return Boolean(status?.logged_in || status?.LoggedIn);
}

function grokAuthSummary(status) {
  if (!status) {
    return '点击 Status 检查当前账号';
  }
  if (grokLoggedIn(status)) {
    const account = status.account_id || status.AccountID || '';
    return account ? `已登录账号 ${account}` : '已登录';
  }
  const message = String(status.message || status.Message || '').trim();
  if (message) {
    return message;
  }
  const activeLogin = String(status.active_login || status.ActiveLogin || '').trim();
  if (activeLogin) {
    return `登录状态：${activeLogin}`;
  }
  if (status.needs_reauth || status.NeedsReauth) {
    return '需要登录或重新授权';
  }
  return '未登录';
}

export function modelAddModeDefaults(state, providers = []) {
  if (state.mode === 'preset') {
    return modelAddPresetDefaults(state);
  }
  if (state.mode === 'custom') {
    return {
      ...state,
      provider: String(state.provider || '').startsWith('custom-') ? state.provider : 'custom-openai',
      type: state.type || 'openai',
      model: state.model || 'model-name',
      api: state.api || 'chat',
      auth: ''
    };
  }
  if (state.mode === 'grok_oauth') {
    const model = !state.model || state.model === 'model-name' ? grokOAuthDefaults.model : state.model;
    const provider = String(state.provider || '').trim();
    return {
      ...state,
      provider: provider.toLowerCase().includes('grok') ? provider : grokOAuthDefaults.provider,
      type: grokOAuthDefaults.type,
      auth: grokOAuthDefaults.auth,
      model,
      api: 'chat',
      api_key: '',
      base_url: '',
      account_id: state.account_id || grokOAuthDefaults.account_id,
      account_name: state.account_name || grokOAuthDefaults.account_name
    };
  }
  return {
    ...state,
    provider: providers.some((provider) => provider.name === state.provider) ? state.provider : providers[0]?.name || '',
    type: '',
    auth: '',
    api: 'chat',
    api_key: '',
    base_url: ''
  };
}

export function modelOptionsForProvider(providers = [], providerName = '', currentModel = '') {
  const provider = providers.find((item) => item.name === providerName);
  const models = [...(provider?.models || [])];
  const selected = String(currentModel || '').trim();
  if (selected && !models.includes(selected)) {
    return [selected, ...models];
  }
  return models;
}

function modelAddPresetDefaults(state) {
  const preset = providerPresets.find((item) => item.provider === state.preset) || providerPresets[0];
  return {
    ...state,
    provider: preset.provider,
    type: preset.type,
    auth: '',
    base_url: preset.base_url || '',
    model: preset.model,
    api: preset.api || 'chat'
  };
}

export function buildModelAddPayload(state, modelConfig) {
  const role = state.role || 'default';
  if (state.mode === 'existing') {
    const providers = modelConfig?.providers || [];
    return {
      role,
      provider: String(state.provider || providers[0]?.name || '').trim(),
      model: String(state.model || '').trim()
    };
  }
  if (state.mode === 'grok_oauth') {
    return {
      role,
      provider: String(state.provider || grokOAuthDefaults.provider).trim(),
      model: String(state.model || grokOAuthDefaults.model).trim(),
      type: grokOAuthDefaults.type,
      auth: grokOAuthDefaults.auth,
      account_id: String(state.account_id || grokOAuthDefaults.account_id).trim()
    };
  }
  if (state.mode === 'preset') {
    const preset = providerPresets.find((item) => item.provider === state.preset) || providerPresets[0];
    return {
      role,
      provider: String(state.provider || preset.provider).trim(),
      model: String(state.model || preset.model).trim(),
      type: preset.type,
      api: preset.api || '',
      api_key: String(state.api_key || '').trim(),
      base_url: String(state.base_url || preset.base_url || '').trim()
    };
  }
  return {
    role,
    provider: String(state.provider || '').trim(),
    model: String(state.model || '').trim(),
    type: String(state.type || 'openai').trim(),
    api: state.type === 'openai' ? String(state.api || 'chat').trim() : '',
    api_key: String(state.api_key || '').trim(),
    base_url: String(state.base_url || '').trim()
  };
}

export function canSubmitModelAdd(state, modelConfig) {
  const payload = buildModelAddPayload(state, modelConfig);
  if (!payload.provider || !payload.model) {
    return false;
  }
  if (state.mode === 'grok_oauth') {
    return Boolean(payload.account_id) && grokLoggedIn(state.grok_status);
  }
  if (state.mode === 'preset') {
    const preset = providerPresets.find((item) => item.provider === state.preset) || providerPresets[0];
    if (preset.requiresKey && !payload.api_key) {
      return false;
    }
  }
  if (state.mode === 'custom' && !payload.base_url) {
    return false;
  }
  return true;
}

export function buildBeginCoCreatePayload({ kind, initial = '', sourceFile = '', mode = '', targetTotalWords = 0 } = {}) {
  const payload = {
    kind,
    initial: String(initial || '').trim()
  };
  if (kind === 'adapt') {
    payload.source_file = String(sourceFile || '').trim();
    payload.mode = String(mode || '').trim();
    return payload;
  }
  if (kind === 'normal' && targetTotalWords > 0) {
    payload.target_total_words = targetTotalWords;
  }
  return payload;
}

export function resolveCoCreateTargetTotalWords(state = {}) {
  const choice = String(state.targetTotalWordsChoice || '5000');
  const raw = choice === 'custom' ? state.customTargetTotalWords : choice;
  const value = Number(String(raw ?? '').trim());
  return Number.isInteger(value) && value > 0 ? value : 0;
}

export function deriveWorkspaceProgress(snapshot, eventRows = []) {
  const completedChapters = numberValue(snapshot, 'CompletedCount', 'completed_count');
  const totalChapters = numberValue(snapshot, 'TotalChapters', 'total_chapters');
  const currentChapter = numberValue(snapshot, 'InProgressChapter', 'in_progress_chapter') ||
    numberValue(snapshot, 'CurrentChapter', 'current_chapter');
  const wordCount = numberValue(snapshot, 'TotalWordCount', 'total_word_count');
  const rawWordBudget = valueByKey(snapshot, 'WordBudget', 'word_budget');
  const wordBudget = objectValue(snapshot, 'WordBudget', 'word_budget');
  const wordBudgetTarget = objectValue(wordBudget, 'Target', 'target');
  const targetWords = numberValue(
    snapshot,
    'TargetTotalWords',
    'target_total_words',
    'TargetWords',
    'target_words',
    'TotalWordBudget',
    'total_word_budget',
    'WordBudgetTotal',
    'word_budget_total'
  ) || numberValue(wordBudget, 'TargetTotalWords', 'target_total_words') ||
    numberValue(wordBudgetTarget, 'TargetTotalWords', 'target_total_words') ||
    numberFromValue(rawWordBudget);
  const statusLabel = textValue(snapshot, 'StatusLabel', 'status_label', 'RuntimeState', 'runtime_state') || 'idle';
  const chapterLabel = totalChapters > 0 ? `${completedChapters}/${totalChapters}` : `${completedChapters}`;
  const currentChapterLabel = currentChapter > 0 ? `Ch. ${currentChapter}` : '-';
  const wordLabel = targetWords > 0
    ? `${formatCompact(wordCount)} / ${formatCompact(targetWords)}`
    : formatCompact(wordCount);
  return {
    statusLabel,
    completedChapters,
    totalChapters,
    currentChapter,
    wordCount,
    targetWords,
    chapterLabel,
    currentChapterLabel,
    wordLabel,
    runningLabel: runningLabelFromSnapshot(snapshot) || runningLabelFromEventRows(eventRows) || 'idle'
  };
}

function runningLabelFromSnapshot(snapshot) {
  const agents = arrayValue(snapshot, 'Agents', 'agents');
  const agent = agents.find(isRunningAgent);
  if (!agent) {
    return '';
  }
  const name = textValue(agent, 'Name', 'name') || 'agent';
  const tool = textValue(agent, 'Tool', 'tool');
  const summary = textValue(agent, 'Summary', 'summary');
  const state = textValue(agent, 'State', 'state') || 'running';
  if (tool) {
    return `${name} / ${tool}`;
  }
  if (summary) {
    return `${name} / ${summary}`;
  }
  return `${name} / ${state}`;
}

function runningLabelFromEventRows(eventRows) {
  const row = [...(eventRows || [])].reverse().find((event) => {
    const payload = eventPayload(event);
    return Boolean(payload?.running || payload?.Running);
  });
  const event = eventPayload(row);
  if (!event) {
    return '';
  }
  const agent = textValue(event, 'agent', 'Agent');
  const category = textValue(event, 'category', 'Category') || 'EVENT';
  const summary = textValue(event, 'summary', 'Summary');
  if (agent && summary) {
    return `${agent} / ${summary}`;
  }
  if (agent) {
    return `${agent} / ${category}`;
  }
  return summary || category;
}

function isRunningAgent(agent) {
  const state = textValue(agent, 'State', 'state').toLowerCase();
  const tool = textValue(agent, 'Tool', 'tool');
  if (tool) {
    return true;
  }
  return Boolean(state && !['idle', 'done', 'complete', 'completed', 'paused'].includes(state));
}

function eventPayload(row) {
  return row?.event || row?.Event || null;
}

function arrayValue(source, ...keys) {
  for (const key of keys) {
    if (Array.isArray(source?.[key])) {
      return source[key];
    }
  }
  return [];
}

function objectValue(source, ...keys) {
  for (const key of keys) {
    const value = source?.[key];
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      return value;
    }
  }
  return null;
}

function valueByKey(source, ...keys) {
  for (const key of keys) {
    if (source?.[key] !== undefined && source?.[key] !== null) {
      return source[key];
    }
  }
  return null;
}

function numberValue(source, ...keys) {
  for (const key of keys) {
    const value = numberFromValue(source?.[key]);
    if (value > 0) {
      return value;
    }
  }
  return 0;
}

function numberFromValue(value) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : 0;
}

function textValue(source, ...keys) {
  for (const key of keys) {
    const value = String(source?.[key] ?? '').trim();
    if (value) {
      return value;
    }
  }
  return '';
}

function isFreshProject(snapshot) {
  if (!snapshot) {
    return false;
  }
  return !String(snapshot.NovelName || snapshot.novel_name || '').trim() &&
    !String(snapshot.Phase || snapshot.phase || '').trim() &&
    Number(snapshot.TotalChapters || snapshot.total_chapters || 0) === 0 &&
    Number(snapshot.CompletedCount || snapshot.completed_count || 0) === 0 &&
    Number(snapshot.TotalWordCount || snapshot.total_word_count || 0) === 0;
}

function optionalNonNegativeInt(value, label) {
  const raw = String(value || '').trim();
  if (!raw) {
    return 0;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error(`${label} 必须是非负整数`);
  }
  return parsed;
}

function formatCompact(value) {
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(Number(value || 0));
}

function formatPercent(part, total) {
  const denominator = Number(total || 0);
  if (!denominator) {
    return 'n/a';
  }
  return `${Math.round((Number(part || 0) / denominator) * 100)}%`;
}

function formatUSD(value) {
  return `$${Number(value || 0).toFixed(4)}`;
}

function formatScore(value) {
  const score = Number(value || 0);
  if (!score) {
    return 'n/a';
  }
  return score.toFixed(1);
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
