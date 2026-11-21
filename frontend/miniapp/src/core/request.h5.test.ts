import { beforeEach, describe, expect, it, vi } from 'vitest'

// H5（没开 mock）下没有微信 code：TOKEN_EXPIRED 直接回登录页，即使 bound 为 true。
vi.hoisted(() => {
  process.env.TARO_ENV = 'h5'
  process.env.TARO_APP_WECHAT_MOCK = 'false'
})

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

import { ApiError } from './errors'
import { request } from './request'
import { startSession, useSession } from './session'
import { getLoginCode, WECHAT_AVAILABLE } from './wechat'

beforeEach(() => {
  taro.reLaunch.mockReset()
  taro.reLaunch.mockResolvedValue({})
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
})

describe('H5', () => {
  it('没有微信能力', async () => {
    expect(WECHAT_AVAILABLE).toBe(false)
    await expect(getLoginCode()).rejects.toThrow()
  })

  it('TOKEN_EXPIRED：不续期，清状态并回登录页', async () => {
    startSession('T1', true)
    taro.request.mockResolvedValue({ statusCode: 401, data: { code: 401, reason: 'TOKEN_EXPIRED' } })
    const err = await request({ method: 'GET', url: '/admin/v1/me' }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(taro.request).toHaveBeenCalledTimes(1)
    expect(taro.login).not.toHaveBeenCalled()
    expect(useSession.getState().token).toBeNull()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })
})
