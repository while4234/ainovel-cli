# 小说改编 Writer 追加规则

本文件只适用于 `novel_context` 返回 `adaptation_mode=true` 的小说改编章节；普通原创、导入续写和仿写项目不使用本文件。

## 先读取当前模式

- 每章先看 `working_memory.adaptation_effective_mode.mode_contract`，只执行当前模式对应的小节。
- `rewrite_policy_rule`、旧 brief 里的模式映射、`source_chapters`、`source_range`、`preserve_events` 都不能覆盖 `adaptation_effective_mode`。
- `preserve_events` 的意思是“需要保留的事件功能/因果锚点”，不是 `preserve_details` 策略本身。

## chapter / preserve_details

- `rewrite_policy=preserve_details` 只适用于 `granularity=chapter`：目标章与来源章一一对应。
- 用户要的不是“全章大改”，也不是“整章几乎照搬”。正确做法是：未受改编目标影响的原文事件、场景承接和细节可以保留；受 brief、人物关系、视角、动机或因果影响的**完整场景单元**必须原创重写成连续新正文。
- “完整场景单元”至少包括触发动作、人物反应、对白或沉默、心理流动、场景因果和段落收束；不能只在原句后补一句，也不能只替换几个形容词。
- `chapter + preserve_details` 不是“原文 + 修改说明”，也不是在原句后面贴补丁。改写内容必须融入原段落的叙述节奏、动作链、对话潜台词、感官细节和角色选择。
- 如果 brief 要求“内心独白”“心理变化”“某角色视角”，必须写成角色的身体反应、停顿、动作选择、对白潜台词、自由间接叙述或符合原叙事人称的心理流动。
- 正文禁止使用提示性括注或补丁标签承载改编内容，例如“（某某内心独白：...）”“（某某心理活动：...）”“某某视角：...”“改编补充：...”。出现这种写法时，视为改编没有自然融入，必须重写该段。
- `check_adaptation` 必须把 `change_evidence` 作为工具参数里的 JSON array 传入；不要只写在 `summary`。每项必须说明哪个来源场景被原创改写、改了什么、如何融入动作/对白/因果/叙事节奏。不要把“加入若干内心独白”当作改编达成的理由。

## arc / full_rewrite

- `granularity=arc` 固定使用 `rewrite_policy=full_rewrite`，`word_tolerance=disabled`。
- `source_chapters` / `source_range` 是主线与卷弧锚点：用来确认关键因果、人物命运和事件顺序，不表示目标章可以复用原文段落。
- 可以按需要读取原文锚点核对事实，但写入 `draft_chapter` 的必须是完整原创正文。
- 不要把 arc 模式说成 `preserve_details`，也不要把 `preserve_events` 理解成“保留原文句子”。

## free / full_rewrite

- `granularity=free` 固定使用 `rewrite_policy=full_rewrite`，`word_tolerance=disabled`。
- `source_chapters` / `source_range` 只是后台覆盖率和必要事实锚点，不表示“本章对应原著第 N 章”，也不要求逐章对照原著。
- 写作优先级：确认后的改编提案、分卷、章节细纲、已写新剧情连续性 > source refs 背景事实 > 原著旧章节顺序。
- 不要因为存在 source refs 就反复调用 `read_chapter(source="source")`；只有缺少具体事实时才读取一次相关原文锚点。
- 不要让原著旧结局、旧章节编号或旧人物状态覆盖已经确认的新结局、新关系推进和已写章节。
