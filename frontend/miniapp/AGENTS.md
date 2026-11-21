# AGENTS.md — 林间 Admin 微信小程序（frontend/miniapp）

Taro 4.3.3 + React 18 + TypeScript，组件库 NutUI-React（Taro 版）4.0.0，状态用 zustand。一套代码编译微信小程序和 H5。后端是本仓的 admin-service，账号、角色、权限都复用管理端的。

仓库级铁律（不吞错、门禁）见根目录 `AGENTS.md`。这里只写小程序自己的约定。

## 命令

在 `frontend/miniapp` 下执行。Node 22（`.nvmrc`），pnpm 12.16.1。

| 任务 | 命令 |
|---|---|
| 安装依赖 | `npx -y pnpm@12.16.1 install --frozen-lockfile` |
| 小程序开发（监听） | `npm run dev:mp-weixin`，产物在 `dist/weapp` |
| 小程序构建 | `npm run build:mp-weixin` |
| H5 开发 | `npm run dev:h5`，端口 15889，`/admin` 转发到 `http://localhost:7788` |
| H5 构建 | `npm run build:h5`，产物在 `dist/h5` |
| 类型检查 / lint / 单测 | `npm run typecheck`、`npm run lint`、`npm test` |

**门禁**：声称完成之前跑完 `typecheck`、`lint`、`test`、`build:mp-weixin`、`build:h5`。CI 是 `.github/workflows/miniapp.yml`，同一组命令。

微信开发者工具导入 `frontend/miniapp` 这个目录（`project.config.json` 的 `miniprogramRoot` 指向 `dist/weapp/`）。编译不依赖开发者工具。

后端不在 7788 端口时：`DEV_PROXY_TARGET=http://localhost:端口 npm run dev:h5`。

## 目录

```
config/                 Taro 构建配置（index.ts 公共，dev.ts / prod.ts 分环境）
.env.development        开发构建的环境变量
.env.production         生产构建的环境变量
scripts/gen-tabbar-icons.mjs   从 SVG 生成 tabBar 的 PNG 图标
src/
  app.ts / app.config.ts / app.scss
  assets/tabbar/        tabBar 图标：SVG 源文件和生成的 PNG
  pages/login/          登录：微信一键登录，可切到账号密码
  pages/home/           首页占位（tab 页）
  pages/mine/           我的：头像、资料、改密、退出（tab 页）
  api/auth.ts           登录、退出、验证码
  api/profile.ts        /me、改密、头像
  core/request.ts       请求封装（带令牌、错误提示、令牌失效后的续期和重放）
  core/session.ts       会话（令牌、bound、用户信息、本地存储）和静默续期的单飞
  core/guard.ts         useRequireLogin：需要登录的页面用
  core/wechat.ts        wx.login、手机号授权、mock
  core/crypto.ts        encryptPassword
  core/errors.ts        ApiError、reason → 中文文案
```

接口和类型是手写的，只写页面实际用到的。接口合同看后端 proto（`backend/api/protos/admin/service/v1/`）或管理端生成的 `frontend/admin/react/src/api/generated/`。

## 环境变量

只有 `TARO_APP_` 开头的变量会进代码。

| 变量 | 说明 |
|---|---|
| `TARO_APP_API_BASE` | 后端地址。H5 留空（同源或 devServer 代理）。小程序必须填完整地址，生产要是 https 的合法域名 |
| `TARO_APP_AES_KEY` | 口令加密的 key，和管理端的 `VITE_AES_KEY` 相同。它是公开常量 |
| `TARO_APP_WECHAT_MOCK` | `true` 时走微信 mock，只在开发构建生效 |
| `TARO_APP_ID` | 小程序 AppID，写进 `dist/weapp/project.config.json` |

## 加一个页面

1. 新建 `src/pages/<name>/index.tsx`、`index.config.ts`（`definePageConfig`）、`index.scss`。
2. 在 `src/app.config.ts` 的 `pages` 里加 `'pages/<name>/index'`。第一项是启动页，不要动。
3. 需要登录才能看的页面，组件开头调一次 `useRequireLogin()`。
4. 接口函数写进 `src/api/`，一律走 `request()`。
5. 样式按 750 设计稿写 px，Taro 会转成 rpx / rem。页面底色写在页面根节点上（`min-height: 100vh` 加 `background`），`page` 选择器在 H5 下不生效。

页面之间的跳转：进 tab 页（首页、我的）用 `Taro.switchTab` 或 `Taro.reLaunch`，`navigateTo` 进不去；进普通页面用 `Taro.navigateTo`。

组件用 NutUI：`import { Button, Cell } from '@nutui/nutui-react-taro'`。已配置按需引入，不要再单独 import 样式文件。主色在 `src/app.scss` 里用 CSS 变量改成了和管理端一样的 `#006be6`。

## 底部 tabBar

`src/app.config.ts` 的 `tabBar` 配了两项：首页、我的。不在 `list` 里的页面（登录页和以后加的普通页面）不显示 tabBar。

加一个 tab：

1. 在 `src/assets/tabbar/` 放一个 `<name>.svg`。24×24 的 viewBox，用 `currentColor` 画线，照着 `home.svg` 写。
2. 跑 `node scripts/gen-tabbar-icons.mjs`，生成 `<name>.png`（未选中）和 `<name>-active.png`（选中），81×81，透明底。脚本用本机的 Chrome 栅格化，不需要装依赖；Chrome 不在默认位置时用 `CHROME_PATH` 指定。
3. 在 `tabBar.list` 里加一项，`iconPath` 和 `selectedIconPath` 指向这两张 PNG。小程序的 tabBar 只收 PNG / JPG，最多 5 项。
4. SVG 和 PNG 一起提交。构建和 CI 不跑这个脚本。

改 tabBar 的颜色要改两处：`app.config.ts` 的 `color` / `selectedColor` 和脚本里的 `COLORS`，然后重新生成。

**H5 下的弹层。** Taro 在 H5 里把每个页面放进一个 `z-index: 0` 的层叠上下文，tabBar 是 `z-index: 500`，页面里的 Popup、Dialog、遮罩默认会被 tabBar 压住。`src/app.scss` 末尾有一条只在 H5 生效的样式把页面容器抬到 tabBar 之上，所以弹层直接用就行，不用自己处理。不要删那条样式。小程序的 tabBar 是原生的，弹层贴着它的上沿显示。

## 请求封装

```ts
import { request } from '@/core/request'

// 普通业务接口：自动带 Authorization
const user = await request<User>({ method: 'GET', url: '/admin/v1/me' })

// 登录类接口：不带令牌，失败不触发续期
await request({ method: 'POST', url: '/admin/v1/miniapp/login/phone', data: { code, phoneCode }, auth: false })
```

请求体的形状和管理端一样：CRUD 接口包 `{ data: {...}, updateMask }`；`body: "*"` 的自定义接口（登录、改密、头像）是扁平 JSON，多包一层 `data` 字段会被后端丢掉。

失败时 `request` 已经做了三件事：按 `reason` 弹中文 Toast、`console.error` 打出 `ApiError`、把 `ApiError` 抛出来。调用方一般只需要 `catch` 住做自己的收尾。不想要默认 Toast 就传 `silent: true`。新的 reason 需要文案时加到 `core/errors.ts` 的 `REASON_TEXT`。

`ApiError` 只有状态码、reason、message、请求方法和路径，可以直接进日志。**不要把请求体、请求头、令牌、微信 code、口令打进日志。**

### 令牌失效时发生什么

只有 `401` 且 reason 是 `TOKEN_EXPIRED` 才进入这套流程，其他错误都不重放。

- 这个微信已绑定账号（登录响应的 `miniapp_bound` 为 `true`）：取一个新的 `wx.login` code 调静默续期，成功后用新令牌把请求重放一次。并发的请求共用一次续期。
- 没有绑定，或者在 H5：清状态，回登录页。
- 续期被明确拒绝（`MINIAPP_NOT_BOUND`、`MINIAPP_CODE_INVALID`、`FORBIDDEN`）：清状态，回登录页。
- 续期遇到 429、503、409 或网络失败：不清状态，请求失败并提示稍后重试。
- 响应回来时用户已经退出、重新登录或换了账号：丢弃，抛 reason 为 `SESSION_DISCARDED` 的 `ApiError`，不提示、不跳转、不重放。判断依据是 `core/session.ts` 的 `loginId`：每次登录和每次清状态都换一个，续期不换。

改这部分逻辑要同时改 `core/request.test.ts` 和 `core/session.test.ts`。

## 会话

- 登录成功调 `startSession(token, bound)`，然后取一次 `/me`。`api/auth.ts` 已经封装好。
- 页面里读用户信息：`const user = useSession((s) => s.user)`。
- 退出调 `api/auth.ts` 的 `logout()`：先清本地状态，再用清之前的令牌通知后端，接口失败也照样回登录页。
- 启动时本地有令牌就留在首页并取 `/me`，没有就去登录页。不会主动静默登录。

## 不吞错

每个 `catch` 至少做到一条：`console.error` / `console.warn` 带上原始错误对象，或者重新抛出。Toast 不算日志。`request` 抛出的错误它自己已经记过日志，页面里再 `console.error` 一次也可以，不要写空的 `catch`。

## 微信 mock

后端用 mock 开发构建时，前端打开 `TARO_APP_WECHAT_MOCK`：

```bash
TARO_APP_WECHAT_MOCK=true npm run dev:h5
```

或者把 `TARO_APP_WECHAT_MOCK=true` 写进 `.env.development.local`（不进版本库）。

打开后 `core/wechat.ts` 不调微信：`wx.login` 的 code 固定为 `mock_openid_dev`，手机号 code 是 `mock_phone_<输入的号码>`。登录页多一个手机号输入框，H5 下也能走一键登录和静默续期。

mock 只在开发构建（`npm run dev:*`）里存在：

- 生产构建（`npm run build:*`）遇到 `TARO_APP_WECHAT_MOCK=true` 直接失败。
- mock 分支都写在 `process.env.NODE_ENV !== 'production'` 这个条件里，生产包里整段被删掉。新增 mock 代码时照这个写法，条件要直接写在使用处，从别的模块 import 一个常量来判断是删不掉的。

## 容易踩的地方

- **手机号授权按钮必须用原生按钮**：`import { Button } from '@tarojs/components'` 加 `openType='getPhoneNumber'`。NutUI 的 `Button` 渲染成 `view`，`openType` 写上去不报错，但小程序里不会触发授权。
- **平台差异用 `process.env.TARO_ENV` 判断**（`'weapp'` 或 `'h5'`）。微信相关的能力先看 `core/wechat.ts` 的 `WECHAT_AVAILABLE`。
- **头像地址是后端的相对路径**，显示前过一遍 `api/profile.ts` 的 `avatarSrc()`。
- **升级 Taro**：全部 `@tarojs/*`、`babel-preset-taro`、`eslint-config-taro` 必须是同一个版本号。NutUI 4 的 peer 依赖只到 React 18，不要升 React 19。
- **`pnpm-workspace.yaml`**：`allowBuilds` 关掉了几个依赖的安装脚本，其中 `@tarojs/cli` 和 `@nutui/nutui-react-taro` 的脚本只做使用统计上报。新依赖带安装脚本时 pnpm 会报 `ERR_PNPM_IGNORED_BUILDS`，看清脚本做什么再决定开不开。
