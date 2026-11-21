import Taro from '@tarojs/taro'
import {
  ApiError,
  errorText,
  isTokenExpired,
  REASON_DISCARDED,
  REASON_NETWORK,
  REASON_RENEW_FAILED,
} from './errors'
import { expireSession, isBound, renewSession, snapshot, type SessionSnapshot } from './session'
import { getLoginCode, WECHAT_AVAILABLE } from './wechat'

/**
 * 请求封装。规则（设计方案 9.3、9.4 和修订 3 第 3 条）：
 *
 * - 自动带 Authorization；`auth: false` 的登录类接口不带，失败也不触发续期。
 * - 非 2xx：Toast 中文文案，console.error 打出 ApiError，再抛给调用方。
 *   ApiError 只有状态码、reason、方法、路径；响应里的 message 和平台的 errMsg 是自由文本，不记录。
 * - 只有 401 且 reason 是 TOKEN_EXPIRED 才考虑续期和重放。每个请求最多重放一次。
 * - 每次发送（首次和重放）都记下当时的 loginId。失败的响应回来时 loginId 已经变了
 *   （退出、重新登录、切换账号）：静默丢弃，不提示、不续期、不重放、不跳转。
 */

const SILENT_LOGIN_PATH = '/admin/v1/miniapp/login/silent'
const TIMEOUT_MS = 15000

export interface RequestOptions {
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  /** 以 / 开头的接口路径，例如 /admin/v1/me。 */
  url: string
  data?: unknown
  headers?: Record<string, string>
  /** false：登录类接口，不带令牌，失败不触发续期。默认 true。 */
  auth?: boolean
  /** 指定用哪个令牌（退出时用清状态前记下的令牌）。指定后不参与续期和重放。 */
  token?: string
  /** true：失败时不弹 Toast，由调用方自己提示。日志照打。 */
  silent?: boolean
}

function baseUrl(): string {
  return process.env.TARO_APP_API_BASE || ''
}

function pathOf(url: string): string {
  return url.split('?')[0]
}

/** 发一次请求。成功返回响应体，失败抛 ApiError。不处理令牌失效。 */
async function send<T>(opts: RequestOptions, token: string | null): Promise<T> {
  const header: Record<string, string> = { ...opts.headers }
  if (token) {
    header.Authorization = `Bearer ${token}`
  }
  const meta = { method: opts.method, path: pathOf(opts.url) }

  let res: Taro.request.SuccessCallbackResult<unknown>
  try {
    res = await Taro.request({
      url: baseUrl() + opts.url,
      method: opts.method,
      data: opts.data,
      header,
      timeout: TIMEOUT_MS,
    })
  } catch {
    // 网络失败、超时。平台给的错误对象和 errMsg 是自由文本，可能带出请求地址和参数，
    // 所以不记录、不往上传，只留下「这个接口网络失败」这一事实。
    throw new ApiError({ ...meta, status: 0, reason: REASON_NETWORK })
  }

  if (res.statusCode >= 200 && res.statusCode < 300) {
    return res.data as T
  }
  // 只取 reason，并且只接受枚举名形式的值。message 不取。
  const body = (res.data ?? {}) as { reason?: unknown }
  throw new ApiError({ ...meta, status: res.statusCode, reason: body.reason })
}

/** 记日志、按需提示，然后把错误原样返回给调用处抛出。 */
function report(err: unknown, opts: RequestOptions, toast = true): unknown {
  console.error(`[request] ${opts.method} ${pathOf(opts.url)} 失败`, err)
  if (toast && !opts.silent) {
    Taro.showToast({ title: errorText(err), icon: 'none' })
  }
  return err
}

function discarded(opts: RequestOptions, cause: unknown): ApiError {
  console.warn(`[request] ${opts.method} ${pathOf(opts.url)} 的响应晚到，会话已变，丢弃`, cause)
  return new ApiError({ status: 401, reason: REASON_DISCARDED, method: opts.method, path: pathOf(opts.url) })
}

/** 向后端换新令牌：只带一个新的 wx.login code。 */
async function exchangeToken(): Promise<string> {
  const code = await getLoginCode()
  const res = await send<{ access_token?: string }>(
    { method: 'POST', url: SILENT_LOGIN_PATH, data: { code }, auth: false },
    null,
  )
  if (!res || !res.access_token) {
    throw new Error('续期响应里没有 access_token')
  }
  return res.access_token
}

/** 发出请求之后有没有退出过或重新登录过。续期换令牌不算。 */
function loginChanged(sent: SessionSnapshot): boolean {
  return snapshot().loginId !== sent.loginId
}

/** 用当前令牌重放一次。重放的结果不再进入续期流程。 */
async function replay<T>(opts: RequestOptions, sent: SessionSnapshot, cause: unknown): Promise<T> {
  const cur = snapshot()
  if (cur.loginId !== sent.loginId || !cur.token) {
    throw discarded(opts, cause)
  }
  try {
    return await send<T>(opts, cur.token)
  } catch (err) {
    // 重放的响应也可能晚到
    if (loginChanged(sent)) {
      throw discarded(opts, err)
    }
    throw report(err, opts)
  }
}

async function afterTokenExpired<T>(opts: RequestOptions, sent: SessionSnapshot, cause: unknown): Promise<T> {
  const cur = snapshot()
  // 发出请求之后退出过或重新登录过：这个响应和当前会话无关。
  if (cur.loginId !== sent.loginId || !cur.token) {
    throw discarded(opts, cause)
  }
  // 同一次登录里别的请求已经续期成功。
  if (cur.token !== sent.token) {
    return replay<T>(opts, sent, cause)
  }
  // 续不了：没有微信环境，或这个微信没有绑定。
  if (!WECHAT_AVAILABLE || !isBound()) {
    expireSession(sent)
    throw report(cause, opts, false)
  }

  const outcome = await renewSession(exchangeToken, sent)
  switch (outcome) {
    case 'renewed':
      return replay<T>(opts, sent, cause)
    case 'rejected':
      // 被拒绝，或换回来的是别的账号的令牌。会话已清、已回登录页、已提示，不重放。
      throw report(cause, opts, false)
    case 'stale': {
      // 续期途中会话变了，按当前状态重新判断一次。
      const now = snapshot()
      if (now.loginId === sent.loginId && now.token && now.token !== sent.token) {
        return replay<T>(opts, sent, cause)
      }
      if (now.loginId !== sent.loginId || !now.token) {
        throw discarded(opts, cause)
      }
      break
    }
    case 'failed':
      break
  }
  // 临时故障：不清状态，下一次请求会再试续期。
  throw report(
    new ApiError({ status: 0, reason: REASON_RENEW_FAILED, method: opts.method, path: pathOf(opts.url) }),
    opts,
  )
}

export async function request<T = unknown>(opts: RequestOptions): Promise<T> {
  if (opts.auth === false || opts.token !== undefined) {
    try {
      return await send<T>(opts, opts.token ?? null)
    } catch (err) {
      throw report(err, opts)
    }
  }

  const sent = snapshot()
  try {
    return await send<T>(opts, sent.token)
  } catch (err) {
    // 先看是不是晚到的：发出之后会话已经换了，这个失败和当前用户无关，什么都不做。
    if (loginChanged(sent)) {
      throw discarded(opts, err)
    }
    if (!isTokenExpired(err)) {
      throw report(err, opts)
    }
    return afterTokenExpired<T>(opts, sent, err)
  }
}
