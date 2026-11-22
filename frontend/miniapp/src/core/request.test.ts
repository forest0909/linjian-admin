import { beforeEach, describe, expect, it, vi } from 'vitest'

const taro = vi.hoisted(() => {
  const storage = new Map<string, unknown>()
  return {
    storage,
    request: vi.fn(),
    login: vi.fn(),
    reLaunch: vi.fn(),
    showToast: vi.fn(),
    getStorageSync: vi.fn((key: string) => storage.get(key)),
    setStorageSync: vi.fn((key: string, value: unknown) => void storage.set(key, value)),
    removeStorageSync: vi.fn((key: string) => void storage.delete(key)),
  }
})
vi.mock('@tarojs/taro', () => ({ default: taro }))

import { ApiError, REASON_DISCARDED, REASON_NETWORK, REASON_RENEW_FAILED } from './errors'
import { request } from './request'
import { clearSession, LOGIN_PAGE, setUser, startSession, useSession } from './session'
import { fakeJwt } from '../test-utils'

// 形状和后端 access token 一样的假 JWT。T* 属于用户 1，U1 属于用户 2。
const T1 = fakeJwt(1, 't1')
const T2 = fakeJwt(1, 't2')
const T3 = fakeJwt(1, 't3')
const U1 = fakeJwt(2, 'u1')

const SILENT = '/admin/v1/miniapp/login/silent'

interface Call {
  url: string
  method: string
  data: unknown
  token: string | null
  resolve: (res: { statusCode: number; data: unknown }) => void
  reject: (err: unknown) => void
}

/** 每个 Taro.request 调用都挂起，由用例决定什么时候、怎么回。 */
let calls: Call[] = []

function ok(data: unknown) {
  return { statusCode: 200, data }
}
function fail(status: number, reason: string) {
  return { statusCode: status, data: { code: status, reason, message: reason, metadata: {} } }
}
const expired = () => fail(401, 'TOKEN_EXPIRED')

function business() {
  return calls.filter((c) => c.url !== SILENT)
}
function renewals() {
  return calls.filter((c) => c.url === SILENT)
}
/** 等微任务跑完，让请求封装走到下一个 await。 */
async function flush() {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve()
  }
}
/** 抓住 Promise 的拒绝结果，避免未处理的 rejection。 */
function settle<T>(p: Promise<T>) {
  const state: { value?: T; error?: unknown; done: boolean } = { done: false }
  p.then(
    (value) => Object.assign(state, { value, done: true }),
    (error) => Object.assign(state, { error, done: true }),
  )
  return state
}
function redirects() {
  return taro.reLaunch.mock.calls.length
}
function token() {
  return useSession.getState().token
}

beforeEach(() => {
  calls = []
  taro.storage.clear()
  taro.request.mockReset()
  taro.request.mockImplementation((req: { url: string; method: string; data: unknown; header: Record<string, string> }) => {
    return new Promise((resolve, reject) => {
      calls.push({
        url: req.url,
        method: req.method,
        data: req.data,
        token: req.header.Authorization ? req.header.Authorization.replace('Bearer ', '') : null,
        resolve,
        reject,
      })
    })
  })
  taro.login.mockReset()
  taro.login.mockResolvedValue({ code: 'wx-code', errMsg: 'login:ok' })
  taro.reLaunch.mockReset()
  taro.reLaunch.mockResolvedValue({})
  taro.showToast.mockReset()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  clearSession()
})

describe('request：普通请求', () => {
  it('带上 Authorization，成功返回响应体', async () => {
    startSession(T1, true)
    const p = request<{ id: number }>({ method: 'GET', url: '/admin/v1/me' })
    await flush()
    expect(calls[0].token).toBe(T1)
    calls[0].resolve(ok({ id: 7 }))
    await expect(p).resolves.toEqual({ id: 7 })
  })

  it('auth: false 的登录类接口不带令牌，自己的 TOKEN_EXPIRED 也不触发续期', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/admin/v1/login', data: {}, auth: false }))
    await flush()
    expect(calls[0].token).toBeNull()
    calls[0].resolve(expired())
    await flush()
    expect(p.error).toBeInstanceOf(ApiError)
    expect(calls).toHaveLength(1)
    expect(token()).toBe(T1)
  })

  it('非 2xx：按 reason 提示中文，日志里的错误对象不含请求体和令牌', async () => {
    startSession(T1, true)
    const p = settle(
      request({ method: 'POST', url: '/admin/v1/me/password', data: { oldPassword: 'secret-old', newPassword: 'secret-new' } }),
    )
    await flush()
    calls[0].resolve(fail(400, 'INVALID_PASSWORD'))
    await flush()
    expect((p.error as ApiError).reason).toBe('INVALID_PASSWORD')
    expect(taro.showToast).toHaveBeenCalledWith({ title: '密码错误，请重新输入', icon: 'none' })
    const logged = JSON.stringify(vi.mocked(console.error).mock.calls.map((args) => args.map((a) => (a instanceof Error ? { ...a, message: a.message } : a))))
    expect(logged).toContain('INVALID_PASSWORD')
    expect(logged).not.toContain('secret-old')
    expect(logged).not.toContain('secret-new')
    expect(logged).not.toContain(T1)
  })

  it('reason 不是 TOKEN_EXPIRED 的 401 不触发续期', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/admin/v1/me' }))
    await flush()
    calls[0].resolve(fail(401, 'UNAUTHORIZED'))
    await flush()
    expect((p.error as ApiError).reason).toBe('UNAUTHORIZED')
    expect(calls).toHaveLength(1)
    expect(taro.login).not.toHaveBeenCalled()
    expect(token()).toBe(T1)
    expect(redirects()).toBe(0)
  })

  it('网络失败的业务请求不重放、不续期', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/admin/v1/me/avatar', data: {} }))
    await flush()
    calls[0].reject({ errMsg: 'request:fail timeout' })
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_NETWORK)
    expect(calls).toHaveLength(1)
    expect(token()).toBe(T1)
  })

  it('503 等其他状态码不重放', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/admin/v1/me' }))
    await flush()
    calls[0].resolve(fail(503, 'SERVICE_UNAVAILABLE'))
    await flush()
    expect((p.error as ApiError).status).toBe(503)
    expect(calls).toHaveLength(1)
  })
})

describe('request：静默续期', () => {
  it('并发的多个请求只触发一次续期，每个最多重放一次', async () => {
    startSession(T1, true)
    const a = request({ method: 'GET', url: '/a' })
    const b = request({ method: 'GET', url: '/b' })
    const c = request({ method: 'GET', url: '/c' })
    await flush()
    business().forEach((call) => call.resolve(expired()))
    await flush()

    expect(renewals()).toHaveLength(1)
    expect(taro.login).toHaveBeenCalledTimes(1)
    expect(renewals()[0].data).toEqual({ code: 'wx-code' })
    expect(renewals()[0].token).toBeNull()

    renewals()[0].resolve(ok({ access_token: T2, miniapp_bound: true }))
    await flush()
    const replays = business().slice(3)
    expect(replays.map((r) => r.url).sort()).toEqual(['/a', '/b', '/c'])
    expect(replays.every((r) => r.token === T2)).toBe(true)
    replays.forEach((r) => r.resolve(ok({ url: r.url })))

    await expect(Promise.all([a, b, c])).resolves.toEqual([{ url: '/a' }, { url: '/b' }, { url: '/c' }])
    expect(token()).toBe(T2)
    expect(useSession.getState().bound).toBe(true)
    expect(taro.storage.get('linjian.miniapp.session')).toEqual({ token: T2, bound: true })
    expect(business()).toHaveLength(6)
    expect(renewals()).toHaveLength(1)
    expect(redirects()).toBe(0)
  })

  it('重放的请求再遇到 TOKEN_EXPIRED：不再续期、不再重放', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    business()[1].resolve(expired())
    await flush()
    expect((p.error as ApiError).reason).toBe('TOKEN_EXPIRED')
    expect(business()).toHaveLength(2)
    expect(renewals()).toHaveLength(1)
    expect(token()).toBe(T2)
  })

  it('续期成功后才到的、用旧 token 的 TOKEN_EXPIRED：用新 token 重放，不再续期', async () => {
    startSession(T1, true)
    const slow = request({ method: 'GET', url: '/slow' })
    const fast = request({ method: 'GET', url: '/fast' })
    await flush()
    const [slowCall, fastCall] = business()
    fastCall.resolve(expired())
    await flush()
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    business()[2].resolve(ok('fast-ok'))
    await expect(fast).resolves.toBe('fast-ok')

    // 续期已经结束，这时旧 token 的 401 才回来
    slowCall.resolve(expired())
    await flush()
    expect(renewals()).toHaveLength(1)
    const replayed = business()[3]
    expect(replayed.url).toBe('/slow')
    expect(replayed.token).toBe(T2)
    replayed.resolve(ok('slow-ok'))
    await expect(slow).resolves.toBe('slow-ok')
    expect(taro.login).toHaveBeenCalledTimes(1)
  })

  it('退出后才到的 TOKEN_EXPIRED：丢弃，不续期，不跳转', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    clearSession()
    calls[0].resolve(expired())
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(calls).toHaveLength(1)
    expect(taro.login).not.toHaveBeenCalled()
    expect(redirects()).toBe(0)
    expect(taro.showToast).not.toHaveBeenCalled()
  })

  it('重新登录（含切换账号）后才到的 TOKEN_EXPIRED：丢弃，不重放，不清新会话', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/submit', data: { n: 1 } }))
    await flush()
    clearSession()
    startSession(U1, true)
    calls[0].resolve(expired())
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(calls).toHaveLength(1)
    expect(taro.login).not.toHaveBeenCalled()
    expect(token()).toBe(U1)
    expect(useSession.getState().bound).toBe(true)
    expect(redirects()).toBe(0)
  })

  it('续期途中退出：新 token 被丢弃，挂起的请求不重放', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    clearSession()
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    expect(token()).toBeNull()
    expect(taro.storage.has('linjian.miniapp.session')).toBe(false)
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(business()).toHaveLength(1)
    expect(redirects()).toBe(0)
  })

  it('续期途中重新登录：新 token 被丢弃，新会话不动，挂起的请求不重放', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    clearSession()
    startSession(U1, false)
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    expect(token()).toBe(U1)
    expect(useSession.getState().bound).toBe(false)
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(business()).toHaveLength(1)
  })

  it.each(['MINIAPP_NOT_BOUND', 'MINIAPP_CODE_INVALID', 'FORBIDDEN'])(
    '续期被拒（%s）：清状态并跳登录页一次',
    async (reason) => {
      startSession(T1, true)
      const a = settle(request({ method: 'GET', url: '/a' }))
      const b = settle(request({ method: 'GET', url: '/b' }))
      await flush()
      business().forEach((call) => call.resolve(expired()))
      await flush()
      renewals()[0].resolve(fail(reason === 'MINIAPP_CODE_INVALID' ? 400 : 403, reason))
      await flush()
      expect(token()).toBeNull()
      expect(useSession.getState().bound).toBe(false)
      expect(taro.storage.has('linjian.miniapp.session')).toBe(false)
      expect(redirects()).toBe(1)
      expect(taro.reLaunch).toHaveBeenCalledWith({ url: LOGIN_PAGE })
      expect((a.error as ApiError).reason).toBe('TOKEN_EXPIRED')
      expect((b.error as ApiError).reason).toBe('TOKEN_EXPIRED')
      expect(business()).toHaveLength(2)
      expect(renewals()).toHaveLength(1)
    },
  )

  it('续期被拒时已经重新登录：不清新会话，不跳转', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    clearSession()
    startSession(U1, true)
    renewals()[0].resolve(fail(403, 'MINIAPP_NOT_BOUND'))
    await flush()
    expect(token()).toBe(U1)
    expect(redirects()).toBe(0)
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
  })

  it.each([
    ['503', () => fail(503, 'MINIAPP_UNAVAILABLE')],
    ['429', () => fail(429, 'TOO_MANY_REQUESTS')],
    ['409', () => fail(409, 'CONFLICT')],
  ])('续期遇到 %s：不清状态，挂起的请求失败并提示稍后重试', async (_name, response) => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve(response())
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_RENEW_FAILED)
    expect(token()).toBe(T1)
    expect(useSession.getState().bound).toBe(true)
    expect(redirects()).toBe(0)
    expect(business()).toHaveLength(1)
    expect(taro.showToast).toHaveBeenLastCalledWith({ title: '网络不稳定，请稍后重试', icon: 'none' })
  })

  it('续期遇到网络失败：不清状态；下一次请求会再试续期', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].reject({ errMsg: 'request:fail' })
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_RENEW_FAILED)
    expect(token()).toBe(T1)
    expect(redirects()).toBe(0)

    const again = request({ method: 'GET', url: '/a' })
    await flush()
    business()[1].resolve(expired())
    await flush()
    expect(renewals()).toHaveLength(2)
    renewals()[1].resolve(ok({ access_token: T2 }))
    await flush()
    business()[2].resolve(ok('ok'))
    await expect(again).resolves.toBe('ok')
  })

  it('wx.login 失败按临时故障处理：不清状态', async () => {
    startSession(T1, true)
    taro.login.mockRejectedValueOnce({ errMsg: 'login:fail' })
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_RENEW_FAILED)
    expect(renewals()).toHaveLength(0)
    expect(token()).toBe(T1)
  })

  it('bound 为 false：不续期，清状态并回登录页一次', async () => {
    startSession(T1, false)
    const a = settle(request({ method: 'GET', url: '/a' }))
    const b = settle(request({ method: 'GET', url: '/b' }))
    await flush()
    business().forEach((call) => call.resolve(expired()))
    await flush()
    expect(taro.login).not.toHaveBeenCalled()
    expect(renewals()).toHaveLength(0)
    expect(token()).toBeNull()
    expect(redirects()).toBe(1)
    expect((a.error as ApiError).reason).toBe('TOKEN_EXPIRED')
    // 第二个响应到的时候会话已经清了，按晚到处理
    expect((b.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(business()).toHaveLength(2)
  })

  it('指定 token 的请求（退出）不参与续期', async () => {
    startSession(T1, true)
    clearSession()
    const p = settle(request({ method: 'POST', url: '/admin/v1/miniapp/logout', data: {}, token: T1, silent: true }))
    await flush()
    expect(calls[0].token).toBe(T1)
    calls[0].resolve(expired())
    await flush()
    expect((p.error as ApiError).reason).toBe('TOKEN_EXPIRED')
    expect(calls).toHaveLength(1)
    expect(taro.showToast).not.toHaveBeenCalled()
    expect(redirects()).toBe(0)
  })
})

/** 把传给 console 的参数连同 Error 的 message、stack 和自有字段一起摊平成字符串。 */
function dumpLogs(): string {
  const all = [...vi.mocked(console.error).mock.calls, ...vi.mocked(console.warn).mock.calls]
  return JSON.stringify(
    all.map((args) => args.map((a) => (a instanceof Error ? { ...a, name: a.name, message: a.message, stack: a.stack } : a))),
  )
}
function dumpError(err: unknown): string {
  const e = err as Error
  return JSON.stringify({ ...e, name: e.name, message: e.message, stack: e.stack })
}

describe('request：续期必须还是同一个账号', () => {
  it('续期换回来的是别的账号的令牌：清状态回登录页，旧的写请求不重放', async () => {
    startSession(T1, true)
    setUser({ id: 1, nickname: '用户一' })
    const p = settle(request({ method: 'POST', url: '/admin/v1/me/avatar', data: { imageBase64: 'AAAA' } }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    // 这个微信已经改绑到用户 2，续期接口签发了用户 2 的令牌
    renewals()[0].resolve(ok({ access_token: U1, miniapp_bound: true }))
    await flush()

    expect(business()).toHaveLength(1)
    expect(calls.some((c) => c.token === U1)).toBe(false)
    expect(token()).toBeNull()
    expect(useSession.getState().user).toBeNull()
    expect(taro.storage.has('linjian.miniapp.session')).toBe(false)
    expect(redirects()).toBe(1)
    expect((p.error as ApiError).reason).toBe('TOKEN_EXPIRED')
  })

  it.each([
    ['载荷里没有 uid', fakeJwt(null, 'x')],
    ['不是 JWT', 'opaque-token'],
  ])('续期换回来的令牌%s：按续期失败处理，清状态回登录页，不重放', async (_name, renewed) => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/submit', data: {} }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve(ok({ access_token: renewed }))
    await flush()
    expect(business()).toHaveLength(1)
    expect(token()).toBeNull()
    expect(redirects()).toBe(1)
    expect((p.error as ApiError).reason).toBe('TOKEN_EXPIRED')
  })

  it('当前令牌取不出 uid（无法核对）：不接受续期结果', async () => {
    startSession('opaque-old-token', true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    expect(business()).toHaveLength(1)
    expect(token()).toBeNull()
    expect(redirects()).toBe(1)
    expect(p.done).toBe(true)
  })

  it('同一个账号的新令牌照常接受', async () => {
    startSession(T1, true)
    const p = request({ method: 'GET', url: '/a' })
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve(ok({ access_token: T3 }))
    await flush()
    business()[1].resolve(ok('ok'))
    await expect(p).resolves.toBe('ok')
    expect(token()).toBe(T3)
  })
})

describe('request：错误对象和日志里没有自由文本', () => {
  it('响应里的 message 和不像枚举名的 reason 都不进错误对象、日志和提示', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/admin/v1/me/contact?debug=QUERY-MARK', data: {} }))
    await flush()
    calls[0].resolve({
      statusCode: 400,
      data: { code: 400, reason: 'phone 13800001111 rejected', message: 'user SENSITIVE-MARK 13800001111 not allowed' },
    })
    await flush()
    const err = p.error as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.reason).toBe('UNKNOWN')
    expect(err.message).toBe('POST /admin/v1/me/contact 400 UNKNOWN')
    for (const text of [dumpError(err), dumpLogs(), JSON.stringify(taro.showToast.mock.calls)]) {
      expect(text).not.toContain('SENSITIVE-MARK')
      expect(text).not.toContain('13800001111')
      expect(text).not.toContain('QUERY-MARK')
    }
    expect(dumpLogs()).toContain('/admin/v1/me/contact')
  })

  it('平台的网络错误（errMsg、message）不进错误对象和日志', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/admin/v1/me' }))
    await flush()
    const platformError = Object.assign(new Error('fetch https://api.example.com/x?token=PLATFORM-SECRET failed'), {
      errMsg: 'request:fail PLATFORM-SECRET',
    })
    calls[0].reject(platformError)
    await flush()
    const err = p.error as ApiError
    expect(err.reason).toBe(REASON_NETWORK)
    expect(err.status).toBe(0)
    expect(dumpError(err)).not.toContain('PLATFORM-SECRET')
    expect(dumpLogs()).not.toContain('PLATFORM-SECRET')
    expect(dumpLogs()).toContain(REASON_NETWORK)
  })

  it('续期接口的自由文本也不进日志', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve({ statusCode: 503, data: { reason: 'MINIAPP_UNAVAILABLE', message: 'wechat secret RENEW-SECRET' } })
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_RENEW_FAILED)
    expect(dumpLogs()).not.toContain('RENEW-SECRET')
  })
})

describe('request：旧会话晚到的失败一律静默丢弃', () => {
  it.each([
    ['401 UNAUTHORIZED', () => fail(401, 'UNAUTHORIZED')],
    ['401 TOKEN_EXPIRED', () => expired()],
    ['500', () => fail(500, 'INTERNAL_SERVER_ERROR')],
  ])('首次请求的 %s 在重新登录后才到：不提示、不跳转、不再发请求', async (_name, response) => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/submit', data: {} }))
    await flush()
    clearSession()
    startSession(U1, true)
    calls[0].resolve(response())
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(taro.showToast).not.toHaveBeenCalled()
    expect(redirects()).toBe(0)
    expect(calls).toHaveLength(1)
    expect(taro.login).not.toHaveBeenCalled()
    expect(token()).toBe(U1)
  })

  it('首次请求的网络失败在退出后才到：不提示', async () => {
    startSession(T1, true)
    const p = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    clearSession()
    calls[0].reject({ errMsg: 'request:fail' })
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(taro.showToast).not.toHaveBeenCalled()
  })

  it.each([
    ['401 TOKEN_EXPIRED', () => expired()],
    ['401 UNAUTHORIZED', () => fail(401, 'UNAUTHORIZED')],
  ])('重放的 %s 在重新登录后才到：不提示、不跳转、不再发请求', async (_name, response) => {
    startSession(T1, true)
    const p = settle(request({ method: 'POST', url: '/submit', data: {} }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    expect(business()).toHaveLength(2)
    // 重放已经发出，这时用户换了账号
    clearSession()
    startSession(U1, true)
    taro.showToast.mockClear()
    business()[1].resolve(response())
    await flush()
    expect((p.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(taro.showToast).not.toHaveBeenCalled()
    expect(redirects()).toBe(0)
    expect(business()).toHaveLength(2)
    expect(renewals()).toHaveLength(1)
    expect(token()).toBe(U1)
  })
})

describe('request：续期和发起它的那次登录绑定', () => {
  it('旧账号的续期还挂着时换了账号：新账号的 TOKEN_EXPIRED 等旧续期结束后发起自己的续期', async () => {
    const U2 = fakeJwt(2, 'u2')
    startSession(T1, true)
    const a = settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    expect(renewals()).toHaveLength(1)

    // A 的续期没回来，用户换成了 B，B 的令牌也过期了
    clearSession()
    startSession(U1, true)
    const b = request({ method: 'GET', url: '/b' })
    await flush()
    business()[1].resolve(expired())
    await flush()
    // 全局串行：A 的续期没结束，B 还没发起
    expect(renewals()).toHaveLength(1)

    // A 的续期回来了：结果丢弃，A 的请求丢弃
    renewals()[0].resolve(ok({ access_token: T2 }))
    await flush()
    expect((a.error as ApiError).reason).toBe(REASON_DISCARDED)
    expect(token()).toBe(U1)

    // 然后 B 发起自己的续期
    expect(renewals()).toHaveLength(2)
    expect(taro.login).toHaveBeenCalledTimes(2)
    renewals()[1].resolve(ok({ access_token: U2 }))
    await flush()
    const replayed = business()[2]
    expect(replayed.url).toBe('/b')
    expect(replayed.token).toBe(U2)
    replayed.resolve(ok('b-ok'))
    await expect(b).resolves.toBe('b-ok')
    expect(token()).toBe(U2)
    expect(redirects()).toBe(0)
  })

  it('旧账号的续期失败也不影响新账号发起自己的续期', async () => {
    startSession(T1, true)
    settle(request({ method: 'GET', url: '/a' }))
    await flush()
    calls[0].resolve(expired())
    await flush()
    clearSession()
    startSession(U1, true)
    const b = settle(request({ method: 'GET', url: '/b' }))
    await flush()
    business()[1].resolve(expired())
    await flush()
    renewals()[0].resolve(fail(403, 'MINIAPP_NOT_BOUND'))
    await flush()
    // A 被拒不能清 B 的会话
    expect(token()).toBe(U1)
    expect(redirects()).toBe(0)
    expect(renewals()).toHaveLength(2)
    expect(b.done).toBe(false)
  })
})
