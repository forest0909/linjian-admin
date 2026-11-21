import { beforeEach, describe, expect, it, vi } from 'vitest'

const taro = vi.hoisted(() => ({
  request: vi.fn(),
  login: vi.fn(),
  reLaunch: vi.fn(),
  showToast: vi.fn(),
  getStorageSync: vi.fn(),
  setStorageSync: vi.fn(),
  removeStorageSync: vi.fn(),
}))
vi.mock('@tarojs/taro', () => ({ default: taro }))

import { clearSession, LOGIN_PAGE, startSession, useSession } from '@/core/session'
import { fakeJwt } from '../test-utils'
import { logout } from './auth'

const T1 = fakeJwt(1, 't1')
const U1 = fakeJwt(2, 'u1')

/** 让退出接口挂起，由用例决定什么时候回。 */
function pendingLogout() {
  let resolve: (res: { statusCode: number; data: unknown }) => void = () => {}
  let reject: (err: unknown) => void = () => {}
  taro.request.mockImplementationOnce(
    () =>
      new Promise((res, rej) => {
        resolve = res
        reject = rej
      }),
  )
  return {
    ok: () => resolve({ statusCode: 200, data: {} }),
    fail: () => resolve({ statusCode: 503, data: { reason: 'SERVICE_UNAVAILABLE' } }),
    networkFail: () => reject({ errMsg: 'request:fail' }),
  }
}

beforeEach(() => {
  taro.request.mockReset()
  taro.reLaunch.mockReset()
  taro.reLaunch.mockResolvedValue({})
  taro.showToast.mockReset()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  clearSession()
})

describe('logout', () => {
  it('先清状态并回登录页，再用记下的令牌通知后端', async () => {
    startSession(T1, true)
    const server = pendingLogout()
    const done = logout()
    // 接口还没回来，本地已经退出并跳转
    expect(useSession.getState().token).toBeNull()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
    expect(taro.reLaunch).toHaveBeenCalledWith({ url: LOGIN_PAGE })
    const req = taro.request.mock.calls[0][0]
    expect(req.url).toBe('/admin/v1/miniapp/logout')
    expect(req.data).toEqual({})
    expect(req.header.Authorization).toBe(`Bearer ${T1}`)

    server.ok()
    await done
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
    expect(taro.showToast).not.toHaveBeenCalled()
  })

  it('接口失败：提示「退出未完全成功」，不再跳转', async () => {
    startSession(T1, true)
    const server = pendingLogout()
    const done = logout()
    server.fail()
    await done
    expect(taro.showToast).toHaveBeenCalledTimes(1)
    expect(taro.showToast).toHaveBeenCalledWith({ title: '退出未完全成功', icon: 'none' })
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })

  it('没有令牌时不调接口', async () => {
    await logout()
    expect(taro.request).not.toHaveBeenCalled()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })

  it('退出接口还没回来就重新登录了：晚到的成功回包不跳转、不动新会话', async () => {
    startSession(T1, true)
    const server = pendingLogout()
    const done = logout()
    startSession(U1, true)
    taro.reLaunch.mockClear()

    server.ok()
    await done
    expect(taro.reLaunch).not.toHaveBeenCalled()
    expect(taro.showToast).not.toHaveBeenCalled()
    expect(useSession.getState().token).toBe(U1)
    expect(useSession.getState().bound).toBe(true)
  })

  it.each(['fail', 'networkFail'] as const)('退出接口还没回来就重新登录了：晚到的失败（%s）不提示、不跳转', async (how) => {
    startSession(T1, true)
    const server = pendingLogout()
    const done = logout()
    startSession(U1, true)
    taro.reLaunch.mockClear()

    server[how]()
    await done
    expect(taro.showToast).not.toHaveBeenCalled()
    expect(taro.reLaunch).not.toHaveBeenCalled()
    expect(useSession.getState().token).toBe(U1)
  })
})
