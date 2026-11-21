# 认证与令牌链路（Authentication & Tokens）参考文档

> **定位**：本仓认证机制的唯一权威说明——登录链路全流程、令牌结构与配置、刷新轮换与 Cookie、
> 限流与图形验证码、会话管理与吊销、白名单端点、运维与已知问题。
> 前端权限消费侧（路由/按钮/字段黑名单）见 [frontend_authority.md](./frontend_authority.md)；
> 数据范围另见 [data_scope_design.md](./data_scope_design.md)；`tenant_id` 遗留列见 [tenant_isolation.md](./tenant_isolation.md)。

## 1. 组件地图

```
前端（三端：react/vue-element/vue-vben）
  ├─ 登录表单（验证码图 + AES 加密口令 + Header 携带验证码）
  ├─ access token：仅内存（zustand/Pinia/模块级 store）
  ├─ refresh token：HttpOnly Cookie（JS 不可读）；refresh_exp cookie（可读）供静默恢复/定时器
  └─ 401 → 单飞刷新队列 → /admin/v1/refresh-token（Cookie 自动携带）→ 重放
后端
  ├─ AuthenticationScheme（authn）+ Authorizer（authz）引擎配置：configs/auth.yaml（authn.jwt / authz.type）
  ├─ internal/data/authenticator.go —— 令牌签发/验签/吊销/轮换/缓存（engine 适配层）
  ├─ internal/service/authentication_service.go —— 登录/刷新/验证码
  ├─ internal/data/login_rate_limiter.go —— Redis 双维度失败计数限流
  └─ pkg/middleware/auth/auth.go —— 每请求认证（验签+缓存核对）+ ViewerContext/Operator 注入
```

## 2. 登录链路（`doGrantTypePassword`，按代码顺序）

```
POST /admin/v1/login（grant_type=password）
  ├─ 0  ctx 复位：NoopContext + privacy.Allow（登录查询绕过租户隔离——显式豁免通道）
  ├─ 1  限流预检：IP + 用户名双维度 Redis 计数（锁定期直接 400）
  ├─ 2  图形验证码：X-Captcha-Id/X-Captcha-Value Header，Redis verify-and-delete（单次有效）
  ├─ 3  单租户：凭证与用户都按 tenant_id = 0 查（旧库遗留的 tid>0 账号登录不了）
  ├─ 4  identifier 反查：输入含 @ → email、纯数字 → mobile，反查真实 username；多行歧义拒绝
  ├─ 5  凭证校验（user_credential_repo.FindUserCredential）：
  │      AES 解密（DefaultAESKey）→ 按 (tenant 0, USERNAME, username) 查凭证 → bcrypt 比对；
  │      用户不存在时跑一次假 bcrypt（恒定时间防枚举）
  │      失败 → 限流计数自增 + 统一 INVALID_PASSWORD 文案（防用户名枚举，真实原因进日志/审计）
  ├─ 6  取用户行：按凭证返回的 user_id 精确 Get
  ├─ 7  纵深防御：用户行 tenant 也必须是 0，否则按用户名或密码错误拒绝
  │      （`authentication_service.go:504-509`——它在取到用户行之后，不在凭证库里）
  ├─ 8  授权丰富（`resolveUserAuthority`，调用点 `:520`）：
  │      **先**查 `user.status != NORMAL` 即拒（`:419`，Forbidden "user is disabled"）；
  │      再 authorizeAndEnrich*（一对一/一对多按 DefaultUserTenantRelationType）：
  │      角色链（user→role / membership→role）→ 权限码集合须含 SystemAccessBackendPermissionCode
  │      → 角色 codes 进 token；fillAdminFlags（平台管理员旗标）；
  │      聚合数据范围（dss/dsu）与字段黑名单（hfs）进 token。
  │      hfs 只在非平台管理员时按角色聚合（见第 3.1 节）
  └─ 9  签发：CreateUserToken（access + refresh，jti，Redis 令牌对入缓存）
         → 会话元数据记录 + last_login 记录（均 best-effort）
         → 限流计数清零
         → refresh 经 HttpOnly Cookie 下发（见第 4 章），响应体只回 access + expires_in
```

错误语义统一化（防枚举）：`normalizeLoginVerifyError`（`authentication_service.go:149-158`）把
USER_NOT_FOUND / USER_FREEZE / INVALID_PASSWORD 归并为同文案。
真实原因保留在服务端日志与登录审计的 FailureReason。

⚠ **一处已知的归并缺口**：口令有效期到期时 `FindUserCredential` 返回的是
`ErrorBadRequest("password expired, please reset your password")`（`user_credential_repo.go:458-465`，
阈值取 sys_config `passwordPolicy.ConfigKeyMaxAgeDays`），而 `normalizeLoginVerifyError` 的 `switch`
只认上面三类，`default` 原样上抛。由于这条错误发生在 bcrypt **比对成功之后**，它实际上告诉调用方
"这个账号的口令是对的、只是过期了"——防枚举在该场景下被削弱。要补的话就是把这条 reason 也纳入归并
（并保留日志侧的真实原因）。

`grant_type=refresh_token` 在 login 端点被拒（引导到专用刷新端点）；
`grant_type=client_credentials` 被拒。机器令牌交换已删除。

### 2.1 口令传输

登录、创建用户、管理员重置密码、个人中心改密走同一条契约：前端 `encryptPassword`
（`base64(AES)`，密钥是构建期的 `VITE_AES_KEY`，与后端 `crypto.DefaultAESKey` 一致），
后端解密后再做复杂度校验，通过后 bcrypt 入库。

| 路径 | 前端 | 后端 |
|---|---|---|
| 登录 | 登录表单 | `FindUserCredential`，`NeedDecrypt: true` |
| 创建用户 | `src/api/hooks/user.ts` 的 `useCreateUser` | `UserService.Create`：非空密码先 `decryptTransportPassword`。用户行、角色/组织/岗位关联和凭据在同一个事务里写。复杂度不够，或凭据写入过程 panic，整笔回滚，不会留下没有密码的用户 |
| 管理员重置 / 更新时改密 | `useUpdateUser`、`useEditUserPassword` | `ResetCredential`，`NeedDecrypt: true` |
| 个人中心改密 | `useUpdateUserProfile` | `ChangeCredential`，`NeedDecrypt: true` |

创建用户没传密码时仍填 `constants.DefaultUserPassword` 明文常量，不拿去解密。
启动播种也传明文，不走这条解密。

## 3. 令牌体系

### 3.1 结构（`authentication/service/v1/user_token.proto`，自描述 JWT）

| claim | 字段 | 说明 |
|---|---|---|
| `uid` | user_id | 用户 ID |
| `tid` | tenant_id | 遗留声明，固定为 0 |
| `cid` / `did` | client_id / device_id | 客户端/设备标识 |
| `sub` | username | 用户名 |
| `roc` | roles | 角色码列表。只含状态为启用的角色；停用角色不参与登录和刷新时的角色码、管理员标志、数据范围、隐藏字段，也不装进 casbin |
| `ipa` | is_platform_admin | 平台管理员旗标（fillAdminFlags 产出） |
| `ds`/`dss`/`dsu` | data_scope(s)/unit_ids | 数据范围聚合（语义见 data_scope_design 第 3 节） |
| `ouid` | org_unit_id | 当前组织单元 |
| `hfs` | hidden_fields | 字段黑名单（"资源.字段"）。平台管理员（`is_platform_admin`）不写入，其余用户按角色聚合。语义见 frontend_authority 字段级一节 |
| `jti` | jti | 令牌对唯一标识（吊销/轮换/会话元数据键） |
| `ct` | client_type | 签发时写入的客户端类型（admin / app）。缺省视为 admin |

### 3.2 配置（`configs/auth.yaml`）与引擎

- `authn.type: jwt`（oidc / preshared_key 配置节为预留，未接线）；
- `authn.jwt.method`：本仓 `configs/auth.yaml:8` 配的是 **RS256**；代码里的兜底值才是 HS256
  （`method` 为空时，`authenticator.go:126-129`）。取值直接用字面量：`WithSigningMethod` 走
  `jwtV5.GetSigningMethod(alg)`（`kratos-authn/engine/jwt v1.1.11` 的 `options.go:16-18`），
  所以合法拼写就是 golang-jwt 注册名——HS/RS/PS/ES 各 256/384/512 与 **`EdDSA`**。
  ⚠ `auth.yaml:5` 的注释把最后一个写成 `Ed25519`，那不是注册名，照抄会得到不支持的算法；
  本仓的 `isAsymmetricMethod`（`:169-174`）也只认 `EDDSA`；对称走 `key`，
  非对称走 PEM `private_key`/`public_key`；
- TTL：`access_token_expires: 5400s`（1.5h）、`refresh_token_expires: 43200s`（12h）——
  protobuf Duration 串，未配置用代码默认；
- **生产密钥注入**：环境变量 `GWA_AUTH_JWT_PRIVATE_KEY` / `GWA_AUTH_JWT_PUBLIC_KEY` /
  `GWA_AUTH_JWT_KEY` 优先于 yaml（yaml 内置开发示例密钥，`authenticator.go`
  `applyJwtKeyOverrides` 应用覆盖；只从 yaml 读到示例私钥时告警，经 `GWA_AUTH_JWT_PRIVATE_KEY`
  注入的就是示例私钥时拒绝启动）；轮换命令见 [backend_deploy.md](./backend_deploy.md)；
- **引擎有 admin 和 app 两套**。`getAuthenticator` 按 `ClientType` 选引擎：admin 引擎签发 access + refresh；app 引擎只签发 access token（不签 refresh、不设 Cookie），有效期同样取 `access_token_expires`。刷新端点仍强制 admin。
  - 令牌里写入 `ct` 声明（`client_type`）。`Authenticate` 验签后读 `ct`：**没有这个声明按 admin**（升级前签发的管理端令牌，在未过期、未吊销、租户为 0 且 Redis 记录还在的情况下继续可用）；值不是 admin 或 app 则拒绝。之后用这个类型拼 `at:{ct}:{uid}:{jti}` 查 Redis，比较完整 token 字符串。
  - **`wechat.yaml` 的 `app_id` 为空时一切 app 签发都拒绝**，包括 `/admin/v1/login` 带 `client_type=app`。多实例部署要先让所有实例升级到能识别 `ct` 的版本，再填 `app_id`。

### 3.3 签发与吊销模型（`internal/data/authenticator.go`）

- `CreateUserToken`：jti → access + refresh 双 JWT → `userTokenCache.AddTokenPair`
  （Redis，键含 clientType+uid+jti，TTL 各按配置）；
- **每请求校验**（`Authenticate`，被 `pkg/middleware/auth` 的 accessTokenChecker 调用）：
  JWT 验签 → 过期检查 → **Redis 缓存核对**（`IsValidAccessToken`）——
  令牌不在缓存（已吊销/未入缓存）即拒。因此**吊销即时生效**（删缓存键），
  密钥轮换 + 吊销 = 全量下线；
- 吊销面：`RevokeUserToken`（按 uid，登出用）、`RevokeTokenByJti`（单会话，在线用户强制下线用）、
  `RevokeUserTokenAllClientTypes`（改密踢人等）。`RevokeToken` 和 `RevokeTokenByJti` 汇总各步的错误后返回，不再只返回最后一步的；
- 吊销钩子带客户端类型：SSE 只在 admin 会话被全量吊销时断流，只吊销 app 令牌（改号、解绑、下线）不影响管理端已建立的 SSE 连接；
- **共享和专用通道**：REST 中间件接受 admin 和 app 两类令牌；SSE 握手和审计导出在入口处要求 `ct` 为 admin，app 令牌返回 401；
- 鉴权中间件校验令牌失败（缺失、无效、过期）的 401 reason 是 `TOKEN_EXPIRED`，和业务层的 401（`UNAUTHORIZED`）区分开，小程序据此触发静默续期。

## 4. 刷新链路（HttpOnly Cookie + 轮换）

### 4.1 Cookie 策略（`authentication_service.go` 头部常量 + `setRefreshCookies`）

| Cookie | 属性 | 用途 |
|---|---|---|
| `refresh_token` | **HttpOnly**、`Path=/admin/v1/refresh-token`、SameSite=Lax、Secure 按 TLS 自适应 | 刷新凭据本体：JS 读不到、仅刷新请求自动携带（纵深防御） |
| `refresh_exp` | 非 HttpOnly、`Path=/`、仅 Unix 秒 | 前端静默恢复/刷新定时器读的过期时间戳（无敏感信息） |

Secure 自适应（`resolveCookieSecure`）：TLS 直连或可信反代 `X-Forwarded-Proto: https`
→ true；明文 HTTP → false（否则浏览器拒收、dev 落不了地）。清 Cookie 按各自 Path 匹配删除。
SameSite=Lax 按站点判断——localhost 不同端口同站，dev 直连后端（vue-element/vben 形态）可落地。

### 4.2 刷新端点（`RefreshToken` → `doGrantTypeRefreshToken`）

- 端点在鉴权白名单内（refresh token 自描述 JWT **独立鉴权**，不依赖 access token）；
- Cookie 取 refresh → `VerifyRefreshToken`：JWT 验签 + 过期 + **Lua 脚本原子
  「验证 RT→删除 RT→旧 RT 入宽限集合」**（`VerifyAndRevokeTokenPair`）——每次刷新都轮换 jti，
  旧 refresh 立即作废；**旧 access 不随轮换删除**（自然存活至自身 TTL）：
  多标签页/多前端共享同一浏览器 cookie jar，轮换即删会把其他标签页刚拿到手的
  在用令牌作废，引发持续互杀的 401 风暴（2026-10-21 定案修复）；
- **宽限窗口**（`RefreshTokenGraceWindow` 60s，`rtg:` 有序集合，score 为该成员自己的过期时间戳）：
  并发刷新对撞时，持有上一枚且尚未过期的 RT 兑换独立新会话，不再判 `INCORRECT_REFRESH_TOKEN`
  逼前端强制登出。后一次轮换只给新成员计 60 秒，不会把更早的旧 RT 一起续期。
  全量吊销（登出、改密、重置密码、用户角色变化、角色授权变更）都经过
  `UserTokenCache.RevokeToken`，同时删除对应的 `rtg` 键，宽限期内的旧 RT 不能再换新令牌。
  窗口外重放仍拒绝；
- **尚未堵住的竞态**：刷新在 Lua 里确认旧 RT 之后、把新令牌写回之前，全量吊销可能已经完成，
  进行中的刷新仍会把新令牌写回去。要挡住它，需要吊销版本号：`RevokeToken` 递增
  `rtv:{ct}:{uid}`，刷新验证时记下版本，写入新令牌时版本不一致就放弃。这要改
  `doGrantTypeRefreshToken` 和登录/刷新共用的 `AddTokenPair`，本轮只记录、不改行为。
  单会话强制下线（`RevokeTokenByJti`）也不删宽限成员：集合按用户而不是按 jti，
  踢一个会话时没有那枚 RT 的原文；
- **代际链**（`lin:{ct}:{uid}:{jti}`，值=上一代 jti，TTL 同 AT）：因旧 AT 不随轮换
  删除，踢下线（`RevokeTokenByJti`）沿链回溯删除整条世系的 AT，保住「踢即全灭」；
- 重新走授权丰富（第 2 章第 9 步同款，角色/数据范围/字段黑名单**随每次刷新重算**——
  这是"档位变更最迟随刷新生效"的机制根源）；
- 会话元数据**继承旧会话的登录时间**（旧元数据已随旧令牌对原子删除，新元数据携带
  loginAt）；宽限兑换出的独立会话无可继承，登录时间取当下；
- 新 refresh 仍走 Cookie，响应体只回新 access。

### 4.3 前端消费（react 实例，`src/bootstrap.ts` + `core/transport/rest/preset-interceptors.ts`）

- 页面加载 bootstrap：读 `refresh_exp` 有效时静默调 `/refresh-token` 换 access
  （"session silently restored via refresh cookie"）；
- 401 拦截：`refreshTokenQueue` 单飞（并发 401 只发一次刷新），成功后重放队列、失败清队列走重登；
  刷新请求自身的 401 不再触发刷新（防死循环）。

## 5. MFA（TOTP）已删除

登录不再做 TOTP 二次验证。个人中心不再提供绑定，用户管理不再提供救援重置。
`sys_user_mfa_factors` 需要在已部署库手工 `DROP`。凭证枚举 `TOTP` / `OTP` 仍留在
`user_credential`，避免存量凭证行扫描失败。登录日志的 `mfa_status` 列保留，新登录不再写入。

## 7. 辅助闸门

### 7.1 图形验证码（`captchaClient`，go-utils 封装）

- `Generate` → id + base64 图 + answer；**必须显式 `Save` 进 Redis**（键
  `gowind:captcha:<captchaId>`）——漏 Save 则 Verify 永远失败；
- `Verify` 为 verify-and-delete（单次有效）；登录侧经 `X-Captcha-Id`/`X-Captcha-Value`
  Header 传递（避免改动 proto 与三端生成代码）；
- `CaptchaEnabled` 常量当前 true；未配置 captchaClient 时 fail-open（可用性优先）。
  自动化测试可直读 Redis 答案（根 AGENTS.md 的 e2e 做法）。

### 7.2 登录限流（`login_rate_limiter.go`）

- 双维度键：`gowind:login:fail:ip:<ip>` / `gowind:login:fail:user:<username>`；
- 阈值 5 次 / 锁定窗口 15 分钟 / 计数 TTL=锁定窗口（**固定窗口，不是滑动**：Lua 只在 `INCR` 结果为 1 时
  `EXPIRE`，`login_rate_limiter.go:46-49`，所以 TTL 从**首次失败**起算、后续失败不续期——该文件 `:19-20`
  的注释写成"滑动窗口"是错的）；`CheckAndIncr` 为 Lua 原子
  （锁定判定→自增→TTL）；任一维度达阈值即锁；
- Redis 不可用 → **fail-open**（防御性增强不阻断登录可用性），仅告警；
- 登录成功 → `Reset` 清零两维度。

### 7.3 登录策略已删除

不再按 IP、时段或设备拦截登录。`sys_login_policies` 需要在已部署库手工 `DROP`。
图形验证码和登录限流仍在，见 7.1 与 7.2。

## 8. 会话管理与在线用户（`online_session_service.go` + `session_meta.go`）

- 会话元数据（签发/刷新时记录，Redis，键 `us:{ct}:{uid}:{jti}`，`user_token_cache.go:33-34`；
  另有刷新令牌 `rt:{ct}:{uid}:{jti}`、黑名单 `bl:{jti}`）：登录时间（轮换继承）、
  clientType、ip/ua 等——「在线用户」「我的会话」两页的数据源；
- ListOnlineSession（平台侧全量）/ ListMyOnlineSession（本人）；
  RevokeMyOnlineSession（自撤销单会话）；**ForceLogoutSession（平台侧强制下线单会话，
  `RevokeTokenByJti` 即时生效）**；
- 密码修改 → 全 clientType 吊销（踢人），**同一事务里删除该用户全部微信绑定**；管理员重置密码（独立接口 `EditUserPassword` 和更新用户时附带密码两条路径）同样如此，并且在写入之前先做目标用户的权限上限检查；
- 手机号实际变化（自助短信验证改号、管理员改号或清号；号码和库里一致时什么都不做）→ 同一事务里删除该用户全部微信绑定，提交后吊销该用户的 app 令牌。管理员改号的检查在仓储的更新事务里按实际要写入的值触发，并同样做权限上限检查；
- app 会话的下线（退出、自助踢会话、强制下线、管理端解绑）统一走 `revokeBinding`：删除这次会话所用的绑定，再吊销该用户全部 app 令牌。详见第 13 章；
- 用户角色集合发生变化 → 全 clientType 吊销。`UserService.Update` 在写入前用
  `ListRoleIDsByUserID` 和请求中的角色集合比较；无 field mask（全量更新）或 mask 含
  `role_ids` / `role_id`，且两边不一致时，更新成功后调用 `RevokeUserTokenAllClientTypes`。
  集合相同，或 mask 只含昵称等其它字段，已签发令牌继续有效。列举当前角色失败时仍吊销。
  吊销失败只记日志，不把已经提交的更新判失败。
- 角色授予的内容发生变化 → 持有该角色的用户全部吊销（含正在操作的人自己，会被踢下线）。
  写口只有 `RoleService.Update` / `Delete`：菜单、接口、权限点、数据范围（含选定组织）、
  字段权限、启用/停用都在 Role 消息上，没有单独的分配 RPC。`Update` 在 `roleRepo.Update`
  改写 mask 之前，用更新前读到的角色和请求比较。只有 mask 里出现、仓库这次真的会写入、
  并且写入后的值和旧值不同的授权字段才吊销。授权字段是 `status`、`data_scope`、`org_units`、
  `permissions`、`field_permissions`、`code`、`type`。`tenant_id` 在角色上不可变，更新写不进非空值；
  只有 mask 把它置空才会 SET NULL，这才算变更。mask 为 nil 时标量按全量比较；这三项关联只有
  mask 显式带上才替换。空 mask 会先追加 `updated_by`，仓库只更新审计字段，不吊销。
  集合忽略顺序和重复。字段权限按 resource 归并，隐藏字段按集合比较，空列表和没有该资源视为相同。
  `org_units` 不在 mask 里时保留原集合（管理端只在 SELECTED_UNITS 档提交这个字段），这不算变更。
  受保护角色被剥掉的 `status`、`code`、`type` 不参与比较。管理端保存会把未改的权限、字段权限、
  code、status、dataScope 一并放进 mask；值和原来一样时只改名称不吊销。
  `Delete` 一律吊销。判定为授权变更的 `Update`，以及 `Delete`，都在写库之前用
  `userRoleRepo.ListUserIDsByRoleIDs`（不过滤过期关联）列出持有人。列举失败直接返回错误，
  不执行更新或删除，避免库里的授权已经换了、旧令牌仍按变更前的快照生效。写成功后再对这份名单逐个
  `RevokeUserTokenAllClientTypes`。吊销失败只记日志，不把已经提交的写操作判失败。
  只改名称等非授权字段时不列举持有人。刷新轮换仍不删除尚未过期的旧 access token；
- `WhoAmI` 返回操作者 uid/username（operator 上下文）；注册入口已整体移除
  （账号一律管理员建，避免半注册孤儿）。

## 9. 免鉴权白名单端点（`rest_server.go` `AddWhiteList`）

| 端点 | 自带防护 |
|---|---|
| Login | 全链闸门（第 2 章） |
| GenerateCaptcha / VerifyCaptcha | 单次有效、无敏感返回 |
| RefreshToken | refresh 自描述 JWT 独立鉴权 + 原子轮换 + 缓存吊销核对 |
| MiniappAuthService.LoginByPhone | 请求额度（IP、AppID、openid 换号三维）+ 服务端兑换两个微信 code + AppID 水印校验 + 手机号精确匹配唯一用户 |
| MiniappAuthService.SilentLogin | 请求额度 + 服务端兑换 `wx.login` code + 必须命中已有绑定 |

白名单**跳过的是整个 `auth.Server` 中间件**（令牌校验、viewer 注入与接口鉴权都在里面），
selector 的语义是"命中白名单 ⇒ 返回 false ⇒ 不执行该中间件"（`kratos-bootstrap/rpc v0.1.3` 的
`whitelist.go:132-144`，装配点 `rest_server.go`）。

不受影响的两项：**参数校验中间件对所有路由生效**（`validate.Validator()` 放在 selector 之外，`rest_server.go:77`，注释写明了理由），**登录/操作审计照记**（`applogging.Server` 在 selector 之前）。

## 10. 运维

| 事项 | 操作 |
|---|---|
| 生产密钥 | `GWA_AUTH_JWT_*` 环境变量注入（yaml 是开发密钥）；轮换=换钥+吊销全量（缓存核对保证即时失效） |
| TTL 调整 | auth.yaml 两字段（Duration 串）；生效于新签发，存量按旧 TTL |
| 自动化登录 | 直读 Redis `gowind:captcha:<captchaId>` 取答案（或临时置 CaptchaEnabled=false 后编译） |
| 限流排查 | Redis 双维度键计数；误锁清理直接删键 |
| 审计 | 全部登录尝试落 `sys_login_audit_logs`（成败/原因/归属地），见 audit-log-producer-design |
| 会话处置 | 在线用户页强制下线（单会话）/ 改密、用户角色集合变化、或角色授权变更（含删除角色）时自动全吊销。只改角色名称、描述、排序不吊销。操作者自己持有该角色时同样被踢下线 |

## 11. 已知问题

| 项 | 现状 | 影响 |
|---|---|---|
| ~~MFA（TOTP）与登录策略~~ | **已删除**：登录不再二次验证，也不再按 IP、时段或设备拦截。登录日志 `mfa_status` 列保留 | 已部署库需手工 `DROP TABLE sys_user_mfa_factors, sys_login_policies`，并删除菜单 `LoginPolicyManagement` |
| ~~`LoginResponse.RefreshToken`/`RefreshExpiresIn` 字段残留~~ | **已清理（2026-10-03）**：proto 删字段 + 三端重生成；核实三端零消费方（refresh 全走 HttpOnly Cookie）。`LoginRequest.refresh_token`（授权请求入参，另一语义）保留 | 无 |
| preshared_key 配置节 | 预留未接线 | 接入前勿在生产配置里误以为已启用 |
| ~~oauth proto（第三方绑定）~~ | **已移除（2026-10-03）**：OAuthService 11 个 RPC 无实现/无注册/三端生成物零引用，微信/QQ 等消费平台绑定不在产品范围 | 无 |
| ~~OIDC 企业登录~~ | **已移除**：环境变量 `SSO_OIDC_*`、登录页按钮、`/auth/sso/callback` 和 `auth.yaml` 的 `authn.oidc` 都已删掉。凭证枚举 `ENTERPRISE_SSO` / `SSO_TOKEN` 仍留在 `user_credential`，避免存量行扫描失败 | 无 |

## 12. 边界（明确不做）

- OIDC 企业登录已移除，不再从环境变量或 `auth.yaml` 的 oidc 节恢复；
- 注册入口（RegisterUser）已移除，不再恢复（孤儿用户教训）；
- login 端点不做 refresh（专用端点）、不做 client_credentials（机器令牌交换已删除）；
- 小程序不签发 refresh token；app 令牌过期后只能靠微信绑定静默续期（第 13 章）；
- 找回密码（`/admin/v1/forgot-password`、`/admin/v1/reset-password-by-code`）已删除。个人中心换绑邮箱和手机仍读 `configs/mail.yaml` 同步发验证码；
- MFA（TOTP）和登录策略已删除。图形验证码和登录限流保留；
- access token 不落 localStorage/sessionStorage（仅内存，刷新即失，靠 refresh Cookie 恢复）。

## 13. 微信小程序登录

接入、配置、本地开发和验收见 [miniapp.md](./miniapp.md)。本章只记令牌和会话层的规则。

### 13.1 接口

| RPC | 方法和路径 | 鉴权 |
|---|---|---|
| `AuthenticationService.Login`（`client_type=app`，可带 `code`） | `POST /admin/v1/login` | 白名单 |
| `MiniappAuthService.LoginByPhone` | `POST /admin/v1/miniapp/login/phone` | 白名单 |
| `MiniappAuthService.SilentLogin` | `POST /admin/v1/miniapp/login/silent` | 白名单 |
| `MiniappAuthService.Logout` | `POST /admin/v1/miniapp/logout` | 登录即可（`selfServiceOperations`），只接受 app 令牌 |
| `MiniappAuthService.ListUserBindings` | `GET /admin/v1/users/{userId}/miniapp-bindings` | 登录 + casbin |
| `MiniappAuthService.DeleteUserBinding` | `DELETE /admin/v1/users/{userId}/miniapp-bindings/{id}` | 登录 + casbin |

两个微信登录接口是 `body: "*"` 的自定义 RPC，请求体是扁平 JSON。管理端 `Logout` 只接受 admin 令牌，小程序 `Logout` 只接受 app 令牌，互相调用返回 400。

### 13.2 绑定

- 绑定存在 `sys_user_credentials`，不改 ent schema：`identity_type=SOCIAL_OAUTH`、`provider=wechat_miniapp`、`provider_account_id=openid`、`status=ENABLED`、`tenant_id=0`，`credential` 字段不设置。不记 UnionID。
- 「一个 openid 只对应一个用户」由现有唯一索引 `(tenant_id, provider, provider_account_id)` 保证；一个用户可以有多行。
- 公开的两个登录接口没有登录身份，只做两种写死条件的查询：按已验证的手机号查用户（`tenant_id = 0 AND mobile = ?`）、按已验证的 openid 查绑定。

### 13.3 令牌和会话

- app 令牌只有 access token，有效期取 `access_token_expires`；会话元数据 TTL 同取 access 有效期。
- `SessionMeta.BindingID` 记录这个会话所用的绑定行 ID（H5 登录和 `miniapp_bound=false` 的登录为 0）。access token 和会话元数据由同一个 Redis 脚本写入。
- **签发时按撤销版本条件写入**：先读 `rv:{app}:{uid}` 记为 v，认证和绑定事务之后，条件脚本比较 v 再写入；版本变了就不写，返回 `CONFLICT`。
- **撤销先提交数据库，再动 Redis**：`RevokeToken(app, uid)` 先推进 `rv`，再删除该用户全部 app 令牌和会话元数据。Redis 失败时接口返回 503，操作可重复执行。

### 13.4 绑定事务

- 绑定在数据库事务里生效：锁用户行 → 重新确认（用户启用；手机路径确认 `mobile` 没变，密码路径确认密码凭证没变）→ 查 openid 的绑定（属于别的用户就回滚返回冲突）→ 没有就插入 → 提交。
- 改密、重置密码、改号、清号的事务同样先锁用户行，改凭证并删该用户全部微信绑定。两类事务串行，所以不会出现「凭证已变、绑定又被写进去」。
- 没有补偿删除：签发失败时绑定保留（它是认证成立之后合法写入的，过时的会被凭证变更的事务删掉）。

### 13.5 失败合同

三条路径在超额、Redis 故障、微信超时、绑定冲突、会话写失败下的响应：

| 情况 | 一键登录 | 账号密码加绑定 | 静默续期 |
|---|---|---|---|
| IP 或 AppID 总量超额 | 429，不调微信 | 登录继续，`miniapp_bound=false`，不调微信 | 429，不调微信 |
| openid 换号超额 | 429（已调过 `jscode2session`，不取 access_token） | 不适用 | 不适用 |
| 额度用的 Redis 故障 | 503 `MINIAPP_UNAVAILABLE` | 降级为 `miniapp_bound=false` | 503 |
| 微信超时或报错 | 503 | 登录继续，`miniapp_bound=false` | 503 |
| 微信 code 无效 | 400 `MINIAPP_CODE_INVALID` | 登录继续，`miniapp_bound=false` | 400 |
| openid 已绑别的账号 | 409 `MINIAPP_BOUND_TO_OTHER` | 409，登录被拒 | 不适用 |
| 绑定事务失败，或归属无法确认 | 503，可重试 | 登录继续，`miniapp_bound=false` | 不适用 |
| 撤销版本已变 | 409 `CONFLICT`，可重试 | 409，可重试 | 409，可重试 |
| 会话 Redis 写失败 | 503，已写的绑定保留 | 503，已写的绑定保留 | 503 |

手机号路径没有别的认证依据，绑定写不进去就不签发；密码路径已通过密码认证，绑定只是附加能力，失败时降级，只有确认 openid 属于别的账号时才拒绝。`miniapp_bound=true` 的条件是：库里存在这个 openid 的绑定行，并且它属于本次登录的用户。

### 13.6 日志和审计

- `LoginRequest.code`、微信登录消息的 `code` 和 `phone_code`、`PhoneVerification.code`、`EmailVerification.code` 都加了脱敏注解；访问日志的兜底字段名单含 `phone_code`（不含 `code`，业务字段也叫 `code`）。
- `pkg/wechat` 只向外返回不含 URL、请求、响应正文和微信 `errmsg` 的 `Error`。日志里 openid 只留前 6 后 4，手机号和邮箱打码。
- 登录审计：`MiniappAuthService.LoginByPhone` 进 `loginOperations`，`MiniappAuthService.Logout` 进登出操作清单；静默续期不进登录审计，和管理端刷新一样。管理端解绑是一次 DELETE，操作审计自动记录。
