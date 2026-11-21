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
    startSession('T1', false)
    expect(useSession.getState()).toMatchObject({ token: 'T1', bound: false, user: null })
    expect(snapshot().loginId).not.toBe(before.loginId)
    expect(taro.storage.get(KEY)).toEqual({ token: 'T1', bound: false })
  })

  it('清状态：token、bound、用户信息和本地存储都清掉，loginId 变化', () => {
    startSession('T1', true)
    setUser({ id: 1 })
    const before = snapshot()
    clearSession()
    expect(useSession.getState()).toMatchObject({ token: null, bound: false, user: null })
    expect(snapshot().loginId).not.toBe(before.loginId)
    expect(taro.storage.has(KEY)).toBe(false)
  })

  it('expireSession 只对当前会话生效，重复调用只跳一次', () => {
    startSession('T1', true)
    const snap = snapshot()
    expect(expireSession(snap)).toBe(true)
    expect(expireSession(snap)).toBe(false)
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)

    startSession('T2', true)
    expect(expireSession(snap)).toBe(false)
    expect(useSession.getState().token).toBe('T2')
  })

  it('续期成功：只换 token，loginId 不变', async () => {
    startSession('T1', true)
    const before = snapshot()
    await expect(renewSession(async () => 'T2')).resolves.toBe('renewed')
    expect(snapshot()).toEqual({ token: 'T2', loginId: before.loginId })
    expect(isCurrent(before)).toBe(false)
    expect(taro.storage.get(KEY)).toEqual({ token: 'T2', bound: true })
  })

  it('续期单飞：进行中再调用不会发起第二次', async () => {
    startSession('T1', true)
    let release: (token: string) => void = () => {}
    const exchange = vi.fn(() => new Promise<string>((resolve) => (release = resolve)))
    const first = renewSession(exchange)
    const second = renewSession(exchange)
    release('T2')
    await expect(Promise.all([first, second])).resolves.toEqual(['renewed', 'renewed'])
    expect(exchange).toHaveBeenCalledTimes(1)

    // 上一次结束后可以再发起
    await renewSession(exchange.mockResolvedValueOnce('T3'))
    expect(exchange).toHaveBeenCalledTimes(2)
  })

  it('续期被拒：清状态并回登录页；临时故障：状态不动', async () => {
    startSession('T1', true)
    const transient = new ApiError({ status: 503, reason: 'MINIAPP_UNAVAILABLE', method: 'POST', path: '/x' })
    await expect(renewSession(async () => Promise.reject(transient))).resolves.toBe('failed')
    expect(useSession.getState().token).toBe('T1')
    expect(taro.reLaunch).not.toHaveBeenCalled()

    const rejected = new ApiError({ status: 403, reason: 'MINIAPP_NOT_BOUND', method: 'POST', path: '/x' })
    await expect(renewSession(async () => Promise.reject(rejected))).resolves.toBe('rejected')
    expect(useSession.getState().token).toBeNull()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })
})
