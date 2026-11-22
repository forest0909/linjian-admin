import { beforeEach, describe, expect, it, vi } from 'vitest'

const taro = vi.hoisted(() => {
  const storage = new Map<string, unknown>([['linjian.miniapp.session', { token: 'SAVED', bound: true }]])
  return {
    storage,
    reLaunch: vi.fn(),
    showToast: vi.fn(),
    getStorageSync: vi.fn((key: string) => storage.get(key)),
    setStorageSync: vi.fn((key: string, value: unknown) => void storage.set(key, value)),
    removeStorageSync: vi.fn((key: string) => void storage.delete(key)),
  }
})
vi.mock('@tarojs/taro', () => ({ default: taro }))

import { ApiError } from './errors'
import {
  clearSession,
  expireSession,
  isCurrent,
  renewSession,
  setUser,
  snapshot,
  startSession,
  useSession,
} from './session'
import { fakeJwt } from '../test-utils'

// 形状和后端 access token 一样的假 JWT。T* 属于用户 1，U1 属于用户 2。
const T1 = fakeJwt(1, 't1')
const T2 = fakeJwt(1, 't2')
const T3 = fakeJwt(1, 't3')
const U1 = fakeJwt(2, 'u1')

const KEY = 'linjian.miniapp.session'

beforeEach(() => {
  taro.reLaunch.mockReset()
  taro.reLaunch.mockResolvedValue({})
  taro.showToast.mockReset()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
})

describe('session', () => {
  it('启动时从本地存储恢复 token 和 bound', () => {
    expect(useSession.getState()).toMatchObject({ token: 'SAVED', bound: true, user: null })
  })

  it('登录：保存 token 和 bound，loginId 变化，旧用户信息清掉', () => {
    setUser({ id: 1, nickname: '旧' })
    const before = snapshot()
    startSession(T1, false)
    expect(useSession.getState()).toMatchObject({ token: T1, bound: false, user: null })
    expect(snapshot().loginId).not.toBe(before.loginId)
    expect(taro.storage.get(KEY)).toEqual({ token: T1, bound: false })
  })

  it('清状态：token、bound、用户信息和本地存储都清掉，loginId 变化', () => {
    startSession(T1, true)
    setUser({ id: 1 })
    const before = snapshot()
    clearSession()
    expect(useSession.getState()).toMatchObject({ token: null, bound: false, user: null })
    expect(snapshot().loginId).not.toBe(before.loginId)
    expect(taro.storage.has(KEY)).toBe(false)
  })

  it('expireSession 只对当前会话生效，重复调用只跳一次', () => {
    startSession(T1, true)
    const snap = snapshot()
    expect(expireSession(snap)).toBe(true)
    expect(expireSession(snap)).toBe(false)
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)

    startSession(T2, true)
    expect(expireSession(snap)).toBe(false)
    expect(useSession.getState().token).toBe(T2)
  })

  it('续期成功：只换 token，loginId 不变', async () => {
    startSession(T1, true)
    const before = snapshot()
    await expect(renewSession(async () => T2, snapshot())).resolves.toBe('renewed')
    expect(snapshot()).toEqual({ token: T2, loginId: before.loginId })
    expect(isCurrent(before)).toBe(false)
    expect(taro.storage.get(KEY)).toEqual({ token: T2, bound: true })
  })

  it('续期单飞：进行中再调用不会发起第二次', async () => {
    startSession(T1, true)
    let release: (token: string) => void = () => {}
    const exchange = vi.fn(() => new Promise<string>((resolve) => (release = resolve)))
    const first = renewSession(exchange, snapshot())
    const second = renewSession(exchange, snapshot())
    release(T2)
    await expect(Promise.all([first, second])).resolves.toEqual(['renewed', 'renewed'])
    expect(exchange).toHaveBeenCalledTimes(1)

    // 上一次结束后可以再发起
    await renewSession(exchange.mockResolvedValueOnce(T3), snapshot())
    expect(exchange).toHaveBeenCalledTimes(2)
  })

  it('续期被拒：清状态并回登录页；临时故障：状态不动', async () => {
    startSession(T1, true)
    const transient = new ApiError({ status: 503, reason: 'MINIAPP_UNAVAILABLE', method: 'POST', path: '/x' })
    await expect(renewSession(async () => Promise.reject(transient), snapshot())).resolves.toBe('failed')
    expect(useSession.getState().token).toBe(T1)
    expect(taro.reLaunch).not.toHaveBeenCalled()

    const rejected = new ApiError({ status: 403, reason: 'MINIAPP_NOT_BOUND', method: 'POST', path: '/x' })
    await expect(renewSession(async () => Promise.reject(rejected), snapshot())).resolves.toBe('rejected')
    expect(useSession.getState().token).toBeNull()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })

  it('续期换回来的令牌属于别的账号：清状态回登录页，不保存', async () => {
    startSession(T1, true)
    await expect(renewSession(async () => U1, snapshot())).resolves.toBe('rejected')
    expect(useSession.getState().token).toBeNull()
    expect(taro.storage.has(KEY)).toBe(false)
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })

  it('快照已经不是当前会话：不发起续期', async () => {
    startSession(T1, true)
    const old = snapshot()
    clearSession()
    startSession(U1, true)
    const exchange = vi.fn(async () => T2)
    await expect(renewSession(exchange, old)).resolves.toBe('stale')
    expect(exchange).not.toHaveBeenCalled()
    expect(useSession.getState().token).toBe(U1)
  })

  it('另一次登录的续期进行中：等它结束再发起自己的，互不共用结果', async () => {
    startSession(T1, true)
    let releaseA: (token: string) => void = () => {}
    const exchangeA = vi.fn(() => new Promise<string>((resolve) => (releaseA = resolve)))
    const a = renewSession(exchangeA, snapshot())

    clearSession()
    startSession(U1, true)
    const exchangeB = vi.fn(async () => fakeJwt(2, 'u2'))
    const b = renewSession(exchangeB, snapshot())
    await Promise.resolve()
    expect(exchangeB).not.toHaveBeenCalled()

    releaseA(T2)
    await expect(a).resolves.toBe('stale')
    await expect(b).resolves.toBe('renewed')
    expect(exchangeA).toHaveBeenCalledTimes(1)
    expect(exchangeB).toHaveBeenCalledTimes(1)
    expect(useSession.getState().token).toBe(fakeJwt(2, 'u2'))
  })
})
