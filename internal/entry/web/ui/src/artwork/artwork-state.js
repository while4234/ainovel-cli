export const initialArtworkState = Object.freeze({
  status: 'idle',
  registry: { version: '', models: [] },
  gateway: null,
  catalog: { volumes: [], chapters: [], characters: [] },
  drafts: [],
  draftsCursor: '',
  promptJobs: [],
  jobs: [],
  assets: [],
  assetsCursor: '',
  applied: [],
  errorCode: '',
  notice: ''
});

export function createArtworkState() {
  return {
    ...initialArtworkState,
    registry: { ...initialArtworkState.registry, models: [] },
    catalog: { volumes: [], chapters: [], characters: [] },
    drafts: [], promptJobs: [], jobs: [], assets: [], applied: []
  };
}

export function artworkReducer(state, action) {
  switch (action.type) {
    case 'loading':
      return { ...state, status: 'loading', errorCode: '', notice: '' };
    case 'loaded':
      return mergeWorkspace({
        ...state,
        status: 'ready',
        registry: action.registry || state.registry,
        gateway: action.gateway?.config || action.gateway || state.gateway,
        catalog: action.catalog || state.catalog,
        errorCode: ''
      }, action.workspace, false);
    case 'workspace':
      return mergeWorkspace({ ...state, status: 'ready', errorCode: '' }, action.workspace, true);
    case 'draft-upsert':
      return { ...state, drafts: upsertById(state.drafts, action.draft) };
    case 'draft-remove':
      return { ...state, drafts: state.drafts.filter((item) => item.id !== action.id) };
    case 'drafts-append':
      return {
        ...state,
        drafts: mergeUnique(state.drafts, action.page?.items || []),
        draftsCursor: action.page?.next_cursor || ''
      };
    case 'assets-append':
      return {
        ...state,
        assets: mergeUnique(state.assets, action.page?.items || []),
        assetsCursor: action.page?.next_cursor || ''
      };
    case 'asset-upsert':
      return { ...state, assets: upsertById(state.assets, action.asset) };
    case 'asset-apply':
      return { ...state, assets: applyAssetSelection(state.assets, action.asset) };
    case 'asset-remove':
      return { ...state, assets: state.assets.filter((item) => item.id !== action.id) };
    case 'gateway':
      return { ...state, gateway: action.config, notice: action.notice || '', errorCode: '' };
    case 'error':
      return { ...state, status: state.status === 'loading' ? 'error' : state.status, errorCode: action.code || 'unknown', notice: '' };
    case 'notice':
      return { ...state, notice: action.notice || '', errorCode: '' };
    default:
      return state;
  }
}

export function hasActiveArtworkJobs(state = {}) {
  const active = new Set(['queued', 'running']);
  return [...(state.jobs || []), ...(state.promptJobs || [])].some((job) => active.has(job.status));
}

export function newTerminalArtworkJobs(previous = {}, next = {}) {
  const terminal = new Set(['succeeded', 'failed', 'interrupted_unknown']);
  const before = new Map([
    ...(previous.jobs || []).map((job) => [`image:${job.id}`, job.status]),
    ...(previous.promptJobs || []).map((job) => [`prompt:${job.id}`, job.status])
  ]);
  return [
    ...pageItems(next.jobs).map((job) => ({ ...job, job_kind: 'image' })),
    ...pageItems(next.prompt_jobs || next.promptJobs).map((job) => ({ ...job, job_kind: 'prompt' }))
  ].filter((job) => terminal.has(job.status) && before.get(`${job.job_kind}:${job.id}`) !== job.status);
}

function pageItems(page) {
  if (Array.isArray(page)) return page;
  return Array.isArray(page?.items) ? page.items : [];
}

export function artworkErrorCode(error) {
  return String(error?.data?.error?.code || error?.code || 'unknown').trim() || 'unknown';
}

export function artworkErrorMessage(errorOrCode) {
  const code = typeof errorOrCode === 'string' ? errorOrCode : artworkErrorCode(errorOrCode);
  return ERROR_MESSAGES[code] || ERROR_MESSAGES.unknown;
}

const ERROR_MESSAGES = Object.freeze({
  gateway_not_configured: '请先在右侧配置 AI2API 网关并保存。',
  invalid_gateway_config: '网关地址、默认模型或超时设置无效，请检查后重试。',
  config_save_failed: '网关设置未能保存，请检查本机配置文件权限。',
  gateway_request_failed: '网关验证失败，请检查地址、网络与密钥后手动重试。',
  gateway_auth_failed: '网关拒绝验证，请更新密钥后手动重试。',
  gateway_timeout: '网关验证超时，请检查地址或调大超时后手动重试。',
  prompt_model_unavailable: '项目提示词模型不可用；仍可手动编辑提示词。',
  prompt_model_failed: 'AI 提示词生成失败；可保留当前内容并手动重试。',
  prompt_empty: '提示词模型没有返回可用内容，请调整项目模型后重试。',
  prompt_too_long: '提示词模型返回内容过长，请改用手动提示词。',
  stale_prompt_confirmation_required: '素材来源已变化，请先确认当前来源，再生成图片。',
  artwork_source_unavailable: '当前范围没有可用的已发布内容，请调整范围。',
  draft_version_conflict: '草稿已被其他请求更新，请重新加载草稿后再编辑。',
  asset_is_applied: '正在使用的图片受保护，不能删除。',
  invalid_cursor: '分页位置已失效，请刷新绘境工作台。',
  artwork_not_found: '该草稿或图片已不存在，请刷新工作台。',
  artwork_download_failed: '图片下载失败，请刷新图库后重试。',
  invalid_artwork_request: '当前草稿信息不完整，请检查范围、模型、尺寸和提示词。',
  unknown: '操作未完成，请刷新后手动重试。'
});

function mergeWorkspace(state, workspace = {}, preserveExpandedPages = false) {
  const firstDraftPage = workspace.drafts?.items || [];
  const firstAssetPage = workspace.assets?.items || [];
  const drafts = preserveExpandedPages ? preserveExpanded(firstDraftPage, state.drafts) : firstDraftPage;
  const assets = preserveExpandedPages ? preserveExpanded(firstAssetPage, state.assets) : firstAssetPage;
  return {
    ...state,
    drafts,
    draftsCursor: preserveExpandedPages && drafts.length > firstDraftPage.length ? state.draftsCursor : workspace.drafts?.next_cursor || '',
    promptJobs: workspace.prompt_jobs?.items || [],
    jobs: workspace.jobs?.items || [],
    assets,
    assetsCursor: preserveExpandedPages && assets.length > firstAssetPage.length ? state.assetsCursor : workspace.assets?.next_cursor || '',
    applied: workspace.applied || []
  };
}

function preserveExpanded(firstPage, current) {
  const ids = new Set(firstPage.map((item) => item.id));
  return [...firstPage, ...current.filter((item) => !ids.has(item.id))];
}

function mergeUnique(current, incoming) {
  return incoming.reduce((items, item) => upsertById(items, item), current);
}

function upsertById(items, item) {
  if (!item?.id) return items;
  const index = items.findIndex((entry) => entry.id === item.id);
  if (index < 0) return [...items, item];
  const next = [...items];
  next[index] = item;
  return next;
}

function applyAssetSelection(items, asset) {
  if (!asset?.id) return items;
  const next = items.map((item) => sameAssetTarget(item, asset)
    ? { ...item, applied: item.id === asset.id }
    : item);
  return upsertById(next, { ...asset, applied: true });
}

function sameAssetTarget(left, right) {
  return left?.work_type === right?.work_type &&
    left?.scope === right?.scope &&
    String(left?.scope_id || '') === String(right?.scope_id || '');
}
