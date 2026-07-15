# PR-05 已知剩余问题：pending migration 写路径自锁

## 当前状态

PR-05 的当前实现已包含改编修订服务、候选稿隔离、原著合同校验、
正式写入 owner capability、普通发布候选绑定、一次性发布/回滚状态和
相关 Store/Host/HTTP 回归测试。

当前代码是完整检查点，但 PR-05 尚未通过最终独立复审。剩余一个已确认的
Major 问题：存在 pending structure migration 时，部分受保护的正式写路径
会重复进入同一个不可重入的 revision transaction，造成永久等待；
`ResetGenerated` 还可能在等待前先删除部分正式文件。

基线提交：`ba28a300339632ae746151381a45ef4cb992dfa1`。

## 准确调用链

### ProgressStore 写路径

```text
ProgressStore.withWriteLock
  -> structureMigration.withRead
  -> pending migration
  -> structureMigration.withFencedRead
  -> RevisionStore.withLegacyMutation
  -> write callback
  -> ProgressStore.withLegacyFormalMutation
  -> RevisionStore.withLegacyMutation（第二次进入）
```

`withRevisionTransaction` 已持有同一项目的不可重入 `sync.Mutex`，第二次进入
会在到达文件锁超时之前永久等待。

相关位置：

- `internal/store/progress.go`：`ProgressStore.withWriteLock`
- `internal/store/structure_migration.go`：`withRead` / `withFencedRead`
- `internal/store/revision_lock.go`：`withRevisionTransaction`

### AdaptationStore 写路径

以下路径使用相反的嵌套顺序：先进入 legacy mutation，再通过迁移感知索引读取
尝试恢复 pending migration，继而再次进入 legacy mutation。

```text
ResetGenerated
  -> withLegacyFormalMutation
  -> clearCanonicalChecks
  -> withIndexRead
  -> pending fenced recovery
  -> withLegacyMutation（第二次进入）

SaveCheck / DeleteCheck
  -> withLegacyFormalMutation
  -> withIndexRead
  -> pending fenced recovery
  -> withLegacyMutation（第二次进入）
```

`ResetGenerated` 在到达嵌套恢复前已经可能删除 plan、proposal、review、runtime、
workflow 和 check 文件，因此不仅会卡住，还可能留下部分删除状态。

pending migration journal 可由 failpoint 或瞬时恢复失败留在同一个 live Store，
并非启动恢复后不可达的理论状态。

## 必须保持的锁与事务契约

- pending migration 恢复和后续正式写入只能获取一次 revision transaction。
- 锁顺序保持：`revision -> migration -> IO`。
- 活动 revision、prepared command 或 publication owner 存在时，必须在任何迁移
  或正式文件变化前拒绝无 owner 写入。
- 保留跨进程序列化和真实 owner 的发布、回滚、恢复能力。
- 不得仅交换现有 wrapper 顺序；必须消除所有等价迁移感知 writer 的重复进入。
- `ResetGenerated` 必须在任何破坏性副作用前完成 pending migration 恢复，或在
  失败时逐字节回滚，不能先删除再等待。

## 修复要求

1. 枚举所有调用 `structureMigration.withRead` / `withIndexRead` 的正式写路径。
2. 让 pending migration 恢复与该次正式写入共享同一个最外层 revision
   transaction，并使用不会再次获取该事务的私有 raw/owned helper。
3. 修复 ProgressStore 全部代表性写入，以及 AdaptationStore 的
   `ResetGenerated`、`SaveCheck`、`DeleteCheck` 和所有等价路径。
4. 普通无 owner 重试在瞬时迁移失败后应恢复一次、写入一次并正常返回。
5. 活动 revision/prepared/publication 所有权下，非法调用必须逐字节不变地拒绝。

## 必须新增的测试

- 使用 failpoint 在同一个 live Store 留下 migration journal。
- 对代表性 ProgressStore writer、`SaveCheck`、`DeleteCheck`、
  `ResetGenerated` 运行带超时的测试并连续执行 `-count=5`。
- 验证不会卡住、恢复与写入各执行一次、状态有效且没有部分删除。
- 验证活动 revision、prepared command、publication owner 下逐字节拒绝。
- 增加跨 Store 实例的重试/恢复覆盖。
- 保留 normal `PublishWithOwner` / `formal_applied` 精确绑定、候选替换拒绝、
  adaptation 与 generic/fake 兼容，以及此前全部 PR-05 合同测试。

建议最终门禁：

```text
focused timeout-backed tests -count=5
go test -p 1 ./... -count=1
go vet -p 1 ./...
changed-file gofmt -d
git diff --check
bootstrap/UI/static isolation
```

## 已验证通过的前序修复

最终只读复审已确认：

- 普通模式 plain `Publish` 在 `formal_applied` 前后均逐字节拒绝。
- 必须使用精确 `PublishWithOwner` 才能完成普通发布。
- 候选、发布身份、prepared-only 和磁盘正式结构替换均被拒绝。
- 被拒绝的发布不会消耗 lifecycle 或 accepted artifacts。
- 精确 owner 发布成功。
- Store、Host、真实 HTTP、adaptation 和 generic/fake 兼容测试通过。

该复审还运行并通过了聚焦 `-count=5`、顺序全量 Go、`go vet`、`gofmt`、
`git diff --check` 以及 bootstrap/UI/static 隔离检查。上述通过结果不覆盖本文件
描述的 pending-migration 写路径，因为尚未添加动态复现测试。
