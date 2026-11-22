/**
 * 接口错误和 reason → 中文文案。
 *
 * ApiError 只带状态码、reason、请求方法和去掉参数的路径，可以直接进日志。
 * 后端响应里的 message 和平台错误的 errMsg 是自由文本，可能带出手机号、令牌之类的内容，
 * 一律不进这个对象；Error.message 用这四个字段按固定格式拼出来。
 */

/** 网络层失败（没有拿到 HTTP 响应）时用的 reason。 */
export const REASON_NETWORK = 'NETWORK_ERROR'
/** 响应晚到、发出它的那次登录已经结束，被请求封装丢弃。调用方不用提示。 */
export const REASON_DISCARDED = 'SESSION_DISCARDED'
/** 续期遇到临时故障。 */
export const REASON_RENEW_FAILED = 'RENEW_FAILED'

export const TEXT_RETRY_LATER = '网络不稳定，请稍后重试'
export const TEXT_SESSION_EXPIRED = '登录已过期，请重新登录'

/** reason 不是大写字母、数字、下划线组成的枚举名时用的占位。 */
export const REASON_UNKNOWN = 'UNKNOWN'

/** 只接受形如枚举名的 reason，其他任何内容（包括自由文本）都换成占位。 */
export function trustedReason(raw: unknown): string {
  return typeof raw === 'string' && /^[A-Z][A-Z0-9_]{0,63}$/.test(raw) ? raw : REASON_UNKNOWN
}

export class ApiError extends Error {
  readonly status: number
  readonly reason: string
  readonly method: string
  readonly path: string

  constructor(init: { status: number; reason: unknown; method: string; path: string }) {
    const reason = trustedReason(init.reason)
    const path = init.path.split('?')[0]
    super(`${init.method} ${path} ${init.status} ${reason}`)
    this.name = 'ApiError'
    this.status = init.status
    this.reason = reason
    this.method = init.method
    this.path = path
  }
}

// 文案和管理端 locales/zh-CN/_core/common.json 的 request.reason 保持一致，只收小程序用得到的。
const REASON_TEXT: Record<string, string> = {
  [REASON_NETWORK]: '网络连接失败，请检查网络后重试',
  [REASON_RENEW_FAILED]: TEXT_RETRY_LATER,
  MINIAPP_CODE_INVALID: '微信授权已失效，请重试',
  MINIAPP_PHONE_NOT_REGISTERED: '该手机号未开通账号，请联系管理员',
  MINIAPP_NOT_BOUND: TEXT_SESSION_EXPIRED,
  MINIAPP_PHONE_AMBIGUOUS: '该手机号对应多个账号，请联系管理员',
  MINIAPP_BOUND_TO_OTHER: '该微信已绑定其他账号，请先在原账号退出登录，或联系管理员解绑',
  MINIAPP_UNAVAILABLE: '微信登录暂不可用，请稍后重试',
  TOKEN_EXPIRED: TEXT_SESSION_EXPIRED,
  UNAUTHORIZED: '未授权，请登录',
  FORBIDDEN: '权限不足',
  BAD_REQUEST: '请求参数错误',
  INVALID_PASSWORD: '密码错误，请重新输入',
  INVALID_CAPTCHA: '验证码错误，请重新输入',
  USER_NOT_FOUND: '用户不存在',
  USER_FREEZE: '用户被冻结，请联系管理员',
  USER_DISABLED: '用户已被禁用',
  USER_LOCKED: '用户已被锁定',
  NOT_FOUND: '请求的资源不存在',
  CONFLICT: '操作冲突，请重试',
  TOO_MANY_REQUESTS: '请求过于频繁，请稍后重试',
  NOT_IMPLEMENTED: '功能未开通',
  UPLOAD_FAILED: '上传失败，请稍后重试',
  FILE_TOO_LARGE: '文件过大',
  INTERNAL_SERVER_ERROR: '服务器内部错误，请稍后重试',
  SERVICE_UNAVAILABLE: '服务暂不可用，请稍后重试',
}

const STATUS_TEXT: Record<number, string> = {
  400: '请求参数错误',
  401: '未授权，请登录',
  403: '权限不足',
  404: '请求的资源不存在',
  409: '操作冲突，请重试',
  429: '请求过于频繁，请稍后重试',
  500: '服务器内部错误，请稍后重试',
  502: '服务暂不可用，请稍后重试',
  503: '服务暂不可用，请稍后重试',
  504: '服务暂不可用，请稍后重试',
}

/** 给用户看的文案：先按 reason 查，再按状态码查，都没有就用通用提示。 */
export function errorText(err: unknown): string {
  if (err instanceof ApiError) {
    return REASON_TEXT[err.reason] ?? STATUS_TEXT[err.status] ?? '请求失败，请稍后重试'
  }
  return '请求失败，请稍后重试'
}

/** 鉴权中间件判定令牌失效。只有这一种 401 会触发静默续期。 */
export function isTokenExpired(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401 && err.reason === 'TOKEN_EXPIRED'
}

/** 续期被明确拒绝：这个微信续不上了，必须重新登录。其他失败都按临时故障处理。 */
export function isRenewRejected(err: unknown): boolean {
  return (
    err instanceof ApiError &&
    (err.reason === 'MINIAPP_NOT_BOUND' || err.reason === 'MINIAPP_CODE_INVALID' || err.reason === 'FORBIDDEN')
  )
}

export function isDiscarded(err: unknown): boolean {
  return err instanceof ApiError && err.reason === REASON_DISCARDED
}
