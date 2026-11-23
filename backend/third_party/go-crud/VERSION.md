# go-crud

收编日期：2026-10-26。来源是本机 Go module cache，不是 GitHub 上的最新提交。

| 模块 | 版本 |
|---|---|
| github.com/tx7do/go-crud/api | v0.0.7 |
| github.com/tx7do/go-crud/audit | v0.0.3 |
| github.com/tx7do/go-crud/cache | v0.0.2 |
| github.com/tx7do/go-crud/entgo | v0.0.57 |
| github.com/tx7do/go-crud/pagination | v0.0.16 |
| github.com/tx7do/go-crud/viewer | v0.0.8 |

本仓修改：import 路径改为 `go-wind-admin/third_party/...`。`pagination.pb.go` 的 `go_package` 描述符长度已按新路径更新。字段说明里的上游 README 链接保持原样。

2026-10-26：`entgo/rule/system.go` 的 `PermissionRule` 去掉 `IsPlatformContext()` 放行，只保留 `IsSystemContext()`。单租户下 `tenant_id == 0` 不是数据范围特权；平台管理员靠令牌里的 `ALL` 在规则内部放行。同文件的 `OwnerOnlyRule`、`SoftDeleteRule`、`DenyFieldsMutationRule`、`LimitFieldAccessRule` 未改。


2026-11-01：`entgo/pagination` 的 Page/Offset/Token 三个分页器不再保存请求级状态。它们挂在 Repository 单例上被并发请求共享，以前 `BuildSelector` 把页码写进共享字段、闭包在执行查询时才读，并发请求会互相改掉对方的 OFFSET/LIMIT（`-race` 报 DATA RACE）。现在每次新建底层分页器算出局部值，闭包只捕获局部值。回归测试：`entgo/pagination/shared_state_test.go`。

2026-11-02：`entgo/client.go` 的 `QueryAllChildrenIds` 给 SQLite 加了分支，和 MySQL 共用同一段递归 CTE（`?` 占位符，SQLite 3.8.3 起支持 `WITH RECURSIVE`）。以前 SQLite 方言下查询串为空，子树收集拿不到后代：菜单和组织单元删除时不会级联，后代成为孤儿行。回归测试：`entgo/client_helpers_test.go` 的 `TestQueryAllChildrenIds_SQLite`，以及 `service/menu_service_sqlite_test.go` 的 `TestMenuServiceSqlite_Delete_CascadesSubtree`。

2026-11-10：`entgo/update/update.go` 新增 `SetJsonFieldUpdateBuilder`，把 JSON 列的删键和写键合成一条 SET（`(COALESCE(col,'{}') - '{k}'::text[]) || jsonb_build_object(...)`）。以前要分别调 `SetJsonNullFieldUpdateBuilder` 和 `SetJsonFieldValueUpdateBuilder`，两者同时生效时对同一列 SET 两次，PostgreSQL 报 multiple assignments to same column；列为 NULL 时 `NULL || jsonb` 仍是 NULL。菜单 meta 的更新改用它。回归测试：`app/admin/service/internal/data/menu_meta_update_test.go`。

同一天：`ExtractJsonFieldKeyValues` 支持标量列表（写成 `jsonb_build_array(...)`），不支持的类型（map、嵌套消息、枚举）连键一起跳过。以前列表字段只追加键不追加值，`jsonb_build_object` 参数成奇数，PostgreSQL 报 argument list must have even number of elements（菜单 meta.authority）。
