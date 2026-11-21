# 微信小程序接入指南

本文写给要把小程序接进来的人：登录怎么走、AppID 怎么配、本地怎么开发调试、真机怎么验收，以及下线、解绑、手机号这几处容易踩的语义。

- 小程序前端在 `frontend/miniapp`（Taro 4 + React 18 + NutUI），规范见 `frontend/miniapp/AGENTS.md`。
- 后端没有另起一套账号：小程序用户就是管理端的用户，权限、数据范围、字段权限都按管理端的 RBAC 走。
- 令牌、会话、吊销的细节见 [authentication.md](./authentication.md) 第 13 章。

## 1. 登录方式

小程序提供两种登录，登录页在微信环境下默认「手机号快捷登录」，可切到账号密码；H5（仅用于验证）只有账号密码。

### 1.1 手机号快捷登录

```
小程序                                后端                                  微信
  wx.login → code
  点「手机号快捷登录」→ getPhoneNumber → phoneCode
  POST /admin/v1/miniapp/login/phone
       {"code":"…","phoneCode":"…"}  ─►  1. 额度检查（IP、AppID）
                                         2. code → openid                  ─► jscode2session
                                         3. phoneCode → 手机号             ─► getuserphonenumber
                                         4. 按手机号找唯一用户
                                         5. 事务里写绑定（openid → 用户）
                                         6. 签发 app 令牌
                                    ◄─  {"access_token":"…","miniapp_bound":true,…}
```

- 两个 code 都由服务端向微信兑换，请求里没有 openid 和手机号字段。服务端会校验手机号响应里的 AppID 水印。
- 手机号必须是中国大陆 11 位号码，并且**精确等于**库里 `sys_users.mobile`。匹配不到、匹配到多个都拒绝，不自动注册。
- 一个 openid 只能对应一个用户。这个微信已经绑了别的账号时拒绝（`MINIAPP_BOUND_TO_OTHER`），不会自动改挂。
- 一个账号可以绑多个微信。

### 1.2 账号密码登录

复用 `POST /admin/v1/login`，加上 `client_type=app`：

```http
POST /admin/v1/login
X-Captcha-Id: 3f2a…
X-Captcha-Value: 7k4p

{"grant_type":"password","client_type":"app","username":"zhangsan","password":"<AES base64>","code":"<wx.login code>"}
```

- 验证码、登录限流、口令策略和管理端完全一样。
- 带 `code`（微信环境）时顺带写入绑定，响应里 `miniapp_bound` 表示绑定是否落库。绑定失败不挡登录，降级为 `miniapp_bound=false`；只有确认这个微信已绑别的账号时才拒绝登录。
- H5 不带 `code`，`miniapp_bound` 恒为 `false`。
- 响应没有 `Set-Cookie`，app 令牌只有 access token，没有 refresh token。

### 1.3 静默续期

令牌过期后（`TOKEN_EXPIRED`），小程序用新的 `wx.login` code 调 `POST /admin/v1/miniapp/login/silent`。服务端按 openid 找绑定、找到用户就签发新令牌，用户无感。

- **长期凭证就是微信绑定本身**。没有绑定（`miniapp_bound=false` 的会话、被解绑、被强制下线）续不了，要回登录页重新登录。
- 被明确拒绝（`MINIAPP_NOT_BOUND`、`MINIAPP_CODE_INVALID`、账号停用）小程序回登录页；429、503、409、网络失败不清登录状态，只提示重试。

### 1.4 退出

`POST /admin/v1/miniapp/logout`（只接受 app 令牌）。退出会删除这次会话所用的那条绑定，并吊销该用户的全部 app 令牌。不影响管理端的会话和 SSE 连接。

## 2. 配置 AppID

### 2.1 `wechat.yaml`

`backend/app/admin/service/configs/wechat.yaml`：

```yaml
miniapp:
  app_id: ""        # 留空表示未启用：app 登录和微信接口全部拒绝
  app_secret: ""
  mock: false       # 只在带 miniapp_mock 编译标签的开发构建里有效
```

- 环境变量 `GWA_WECHAT_MINIAPP_APP_ID`、`GWA_WECHAT_MINIAPP_APP_SECRET` 优先于文件。**生产用环境变量注入 Secret，填了 Secret 的 yaml 副本不要提交**（放 `configs/local/` 覆盖层，已 gitignore）。
- 文件不存在按未启用处理；文件存在但解析失败，启动报错。
- 配了 `app_id` 没配 `app_secret`（且不是 mock 构建），启动报错。
- 启动时读一次，改了要重启。
- `app_id` 为空时，**一切** app 签发都拒绝，包括 `/admin/v1/login` 带 `client_type=app`。
- Docker Compose 部署里怎么把这些带进容器，见 [backend_deploy.md](./backend_deploy.md) 的「微信小程序登录」。

### 2.2 开启顺序

多实例部署时，**先让所有实例升级到能识别令牌 `ct` 声明的版本，再填 `app_id` 开启**。否则旧实例会按管理端令牌去校验小程序令牌。

### 2.3 更换 AppID

绑定是 `openid → 用户`，而 openid 随 AppID 变化。更换 AppID 后旧绑定全部作废，要手工清掉：

```sql
DELETE FROM sys_user_credentials
WHERE provider = 'wechat_miniapp' AND tenant_id = 0;
```

### 2.4 小程序后台要配的东西

这些在微信公众平台配，仓库里没有对应配置：

- 服务器域名：request 合法域名填后端的 HTTPS 域名。
- 用户隐私保护指引（手机号、头像等用到的信息），以及登录页展示协议链接。
- 手机号快速验证组件的资费和额度：以小程序后台为准，接入时核对。
- 手机号快速验证组件需要已认证的非个人主体小程序。

## 3. 本地开发和调试

没有真实 AppID 也能把整条链路跑起来：后端用 mock 微信客户端，前端用 H5。

### 3.1 后端 mock 构建

```bash
# backend/
make -C app/admin/service run-miniapp-mock   # 带 miniapp_mock 编译标签；wechat.yaml 里 mock: true
```

- mock 实现在带编译标签 `miniapp_mock` 的文件里，**正式制品（`make build`、Docker 镜像）不带这个标签**；默认构建读到 `mock: true` 会拒绝启动，不管有没有配 Secret。
- mock 构建启动时会打醒目的 warn。**只连本机的开发库和开发 Redis**，不要把它指到共享或生产的库。
- mock code 的约定：

  | 入参 | 结果 |
  |---|---|
  | `code = mock_openid_<串>` | openid 为 `<串>` |
  | `phoneCode = mock_phone_<号码>` | 区号 86 加该号码 |
  | 其他 | 按 code 无效处理（`MINIAPP_CODE_INVALID`） |

- 编译检查：`go build -tags miniapp_mock ./...`。

### 3.2 小程序前端

```bash
# frontend/miniapp/
npm run dev:h5            # H5 devServer，/admin 代理到 :7788
npm run dev:mp-weixin     # 监听编译到 dist/weapp，用微信开发者工具打开 project.config.json
npm run build:mp-weixin   # 产物 dist/weapp
npm run build:h5          # 产物 dist/h5
```

| `.env.*` 变量 | 说明 |
|---|---|
| `TARO_APP_API_BASE` | 后端地址。H5 留空，用同源相对路径 |
| `TARO_APP_AES_KEY` | 口令传输密钥，和管理端 `VITE_AES_KEY` 相同。它会打进前端包，视为公开，传输保密靠生产 HTTPS |
| `TARO_APP_WECHAT_MOCK` | 开发用。**生产构建里为 `true` 时构建直接失败** |

登录账号：全新环境是 `admin / Abcd@1234`（图形验证码答案可在 Redis 里按 `gowind:captcha:<captchaId>` 读）。

### 3.3 用 curl 走一遍 mock

```bash
# 一键登录：mock_phone_ 后面换成某个用户在后台里的手机号
curl -s -X POST localhost:7788/admin/v1/miniapp/login/phone \
  -H 'Content-Type: application/json' \
  -d '{"code":"mock_openid_x","phoneCode":"mock_phone_13800000000"}'
# → {"token_type":"bearer","access_token":"…","expires_in":"5400","miniapp_bound":true}
# 注意 expires_in 是 int64，线上是字符串

# 静默续期
curl -s -X POST localhost:7788/admin/v1/miniapp/login/silent \
  -H 'Content-Type: application/json' -d '{"code":"mock_openid_x"}'
```

这两个接口是 `body: "*"` 的自定义 RPC，请求体是**扁平 JSON**，不要包 `{ "data": … }`。

## 4. 手机号和账号数据

### 4.1 手机号就是登录凭证

一键登录认的是 `sys_users.mobile`，所以改手机号和改密码一样，被当作改凭证管：

- **自助改号只能走短信验证码**（个人中心「账号绑定」页）。资料页的手机号是只读的。
- **管理员改号、清号、重置密码**都会检查目标用户的权限上限：低权限的管理员不能给数据范围内的高权限账号改手机号或重置密码。
- 改手机号、清手机号、改密、重置密码，会**在同一个事务里删除该用户全部微信绑定**，并吊销该用户的 app 令牌。该用户的小程序要重新登录。
- 第一期只接受中国大陆 11 位号码。号码规范化的规则是：去掉首尾空格和 `+86` 前缀后必须是 `^1[3-9]\d{9}$`。
- 手机号不加唯一约束；改号时在事务里检查没有别的用户在用这个号码（`CONFLICT`）。存量重复的号码在一键登录时被拒绝（`MINIAPP_PHONE_AMBIGUOUS`），提示联系管理员。

### 4.2 手机号要录成 11 位纯数字

匹配是精确相等。库里已有的、录成带空格、带 `+86` 或带区号的手机号**匹配不上**，一键登录会得到 `MINIAPP_PHONE_NOT_REGISTERED`。开启功能前把拟开放账号的手机号清洗成 11 位纯数字。

### 4.3 上线前的数据核对

开启功能前，管理员要核实拟开放账号的手机号确实是本人在用的号码，清掉离职人员和已回收号码：已回收的号码被别人拿到后，对方可以用这个号码一键登录对应账号。

### 4.4 短信渠道

个人中心改手机号读 `configs/mail.yaml` 的短信配置。没配短信渠道时，发验证码返回 `NOT_IMPLEMENTED`，页面显示「未配置短信，请联系管理员修改手机号」，由管理员在用户管理里代改。

## 5. 下线和解绑的语义

**下线只有一种做法：删除这次会话所用的那条绑定，再吊销该用户的全部 app 令牌。** 下面四个入口走同一个函数：

| 入口 | 谁用 |
|---|---|
| 小程序「退出登录」 | 用户自己 |
| 个人中心「我的会话」踢掉某个 app 会话 | 用户自己 |
| 在线用户页「强制下线」某个 app 会话 | 管理员 |
| 用户详情「微信绑定」卡片的「解绑」 | 管理员 |

可观察的结果：

- 被下线的那个微信：令牌立刻失效，续期被拒（绑定没了），必须重新一键登录或输账号密码。
- 同一用户的其他微信：令牌也失效，但它们各自的绑定还在，下一次请求会静默续期，用户无感。
- 管理端会话和 SSE 连接：不受影响。
- H5 登录和 `miniapp_bound=false` 的会话没有绑定，下线时只吊销该会话的令牌。
- 解绑是幂等的：绑定已经不存在时仍然返回成功，并照常吊销令牌。

**下线和解绑不能阻止一个手机号仍然匹配的用户重新点一键登录。** 要彻底切断某人的小程序访问：停用账号，或改掉他的手机号（改号会删光该用户的全部绑定）。

角色变更和停用不删绑定：角色变更后续期会重新计算权限；停用的账号在续期时被拒绝。

## 6. 管理端怎么用

| 位置 | 功能 |
|---|---|
| 用户详情 → 基本信息 →「微信小程序绑定」 | 列出该用户的绑定（openid 只显示前 6 后 4）和绑定时间；「解绑」带确认框，确认文案说明该用户的小程序会话将全部失效、其他已绑定的微信会自动重新登录 |
| 个人中心 → 基本设置 | 手机号只读 |
| 个人中心 → 账号绑定 | 手机条目的「绑定 / 更换手机」：输入新号码 → 发短信验证码 → 输入验证码 → 提交。提交后微信绑定被清空，小程序需重新登录 |
| 在线用户 | `app` 类型的会话可以强制下线，语义见第 5 节 |

权限：

- 两个绑定接口（`GET`、`DELETE /admin/v1/users/{userId}/miniapp-bindings`）要在**角色管理里明确授权**。新端点要先 `make openapi` 再重启，Api 表和权限点才会同步（见根 `AGENTS.md`）。
- 目标用户在操作者的数据范围之外时返回 404；解绑还会检查目标用户的权限上限。
- 小程序令牌走和管理端同一条中间件，casbin、数据范围、字段权限原样生效；用户仍然需要「访问后台」权限点。

## 7. 错误 reason

| reason | HTTP | 小程序提示 | 触发 |
|---|---|---|---|
| `MINIAPP_CODE_INVALID` | 400 | 微信授权已失效，请重试 | code 无效、已使用、过期；手机号水印的 AppID 不符 |
| `MINIAPP_PHONE_NOT_REGISTERED` | 403 | 该手机号未开通账号，请联系管理员 | 没有匹配的用户；号码不是中国大陆 11 位 |
| `MINIAPP_NOT_BOUND` | 403 | 登录已过期，请重新登录 | 静默续期时这个微信没有绑定 |
| `MINIAPP_PHONE_AMBIGUOUS` | 409 | 该手机号对应多个账号，请联系管理员 | 匹配到多个用户 |
| `MINIAPP_BOUND_TO_OTHER` | 409 | 该微信已绑定其他账号，请先在原账号退出登录，或联系管理员解绑 | openid 已绑定别的用户 |
| `MINIAPP_UNAVAILABLE` | 503 | 微信登录暂不可用，请稍后重试 | 未配置 AppID、微信超时或报错、Redis 不可用、绑定事务失败 |
| `TOKEN_EXPIRED` | 401 | （触发静默续期） | 鉴权中间件校验令牌失败（缺失、无效、过期、已吊销） |

另外沿用的：账号停用 `FORBIDDEN`；超过请求额度 `TOO_MANY_REQUESTS`；签发时撤销版本已变 `CONFLICT`（重试即可）。

请求额度（成功和失败都计数）：同一 IP 每分钟 60 次、同一 AppID 每分钟 600 次、同一 openid 每 10 分钟换号 5 次。

## 8. 已知风险（接入方需要知道）

- **phoneCode 被截获时可借此建立绑定。** 微信的两个接口没有字段证明 `wx.login` 的 code 和手机号的 code 出自同一个微信号。截获了别人尚未使用的 phoneCode 的人，可以配上自己的 `wx.login` code 登录对方账号并绑上自己的微信。已有的限制：code 在 HTTPS 里传输、校验 AppID 水印、code 不进日志、一个 openid 只能绑一个账号。受害者改密或改手机号后绑定失效。本期接受这个风险，不加二次认证。
- **Redis 故障时已签发的 app 令牌还能用。** 撤销的数据库部分已提交、Redis 删除失败时，旧 access token 在自己的有效期内（默认 1.5 小时）仍被接受。接口返回 503，重试即可清掉；绑定已删，这些令牌过期后续不上。自助改密是例外：密码已经改了，提示会说明密码已修改、请重新登录。
- 下线会让同一用户的其他微信重新续期一次（见第 5 节）。

## 9. 验收清单

### 9.1 本地：H5 加 mock 后端

没有真实 AppID 时的验证范围。后端用 `make -C app/admin/service run-miniapp-mock`，前端用 `npm run dev:h5`。

1. 用 `admin / Abcd@1234` 加验证码登录，进首页。
2. 「我的」显示用户信息；换头像；改密码后回到登录页。
3. 退出登录回到登录页；此时管理端的登录没有被踢。
4. 把本地 token 改坏后发请求，回到登录页。
5. curl 调一键登录，传 `mock_openid_x` 和 `mock_phone_<某用户手机号>`，拿到 token 且 `miniapp_bound` 为 `true`；再调静默续期拿到新 token。
6. 换一个没人用的号码得到 `MINIAPP_PHONE_NOT_REGISTERED`；用同一个 openid 登录另一个用户得到 `MINIAPP_BOUND_TO_OTHER`。
7. 管理端用户详情页看到绑定；解绑后静默续期得到 `MINIAPP_NOT_BOUND`。
8. 重新一键登录后，在管理端「在线用户」强制下线这个 `app` 会话，静默续期被拒。
9. 管理端个人中心：资料页手机号只读；「账号绑定」页在没配短信时显示找管理员代改的提示。

### 9.2 接入环境就绪后的真机验收

需要已认证的非个人主体小程序、真实 AppID 和 Secret、HTTPS 合法域名。真实微信接口的行为差异（mock 覆盖不到的部分）要到这一步才能发现。

| 项 | 环境 | 通过 |
|---|---|---|
| `dist/weapp` 在开发者工具里打开，三个页面（登录、首页、我的）正常 | 开发者工具 | ☐ |
| 账号密码登录并写入绑定；令牌过期后静默续期 | 开发者工具，真实 AppID | ☐ |
| 手机号快捷登录全流程；手机号不在后台时的提示 | 真机 | ☐ |
| 同一部手机切换微信号后打开小程序，回到登录页而不是续到别人的会话 | 真机 | ☐ |
| 配了短信渠道时，个人中心改手机号全流程；改号后小程序要重新登录 | 浏览器 | ☐ |
| 合法域名、用户隐私保护指引、登录页的协议链接 | 小程序后台 | ☐ |
| 手机号组件的资费和额度 | 小程序后台 | ☐ |

建议顺序：先填 `wechat.yaml`（或环境变量）→ 重启后端 → 开发者工具里验账号密码登录和续期 → 真机验手机号登录 → 最后核对小程序后台的配置。

## 10. 排障

| 现象 | 先看 |
|---|---|
| 所有小程序登录都返回 `MINIAPP_UNAVAILABLE` | `app_id` 是不是空（未启用）；后端到微信的网络；Redis 是否可用 |
| 一键登录返回 `MINIAPP_PHONE_NOT_REGISTERED` | 库里这个人的手机号是否录成 11 位纯数字；号码是不是大陆号码 |
| 返回 `MINIAPP_PHONE_AMBIGUOUS` | 库里是否有多个用户用了同一个手机号 |
| 业务接口 401 且 reason 是 `TOKEN_EXPIRED` | 令牌过期或已被吊销（改密、改号、解绑、强制下线）；小程序会尝试静默续期 |
| 静默续期返回 `MINIAPP_NOT_BOUND` | 这个微信没有绑定（被解绑、被强制下线、改过手机号或密码）；重新登录即可 |
| 管理端新增的绑定接口 403 | 角色里没授权这两个接口，或新端点还没同步（先 `make openapi` 再重启） |
| 后端拒绝启动，提示 mock | 正式二进制读到了 `mock: true`；改回 `false`，或用 `run-miniapp-mock` 构建 |
