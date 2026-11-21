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

import { clearSession, startSession, useSession } from '@/core/session'
import { avatarSrc, loadUser } from './profile'

const expired = { statusCode: 401, data: { code: 401, reason: 'TOKEN_EXPIRED' } }

beforeEach(() => {
  taro.request.mockReset()
  taro.login.mockReset()
  taro.login.mockResolvedValue({ code: 'wx-code', errMsg: 'login:ok' })
  taro.reLaunch.mockReset()
  taro.reLaunch.mockResolvedValue({})
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  clearSession()
})

describe('loadUser', () => {
  it('取到用户信息后写进会话', async () => {
    startSession('T1', true)
    taro.request.mockResolvedValueOnce({ statusCode: 200, data: { id: 1, nickname: '管理员' } })
    await loadUser()
    expect(useSession.getState().user).toEqual({ id: 1, nickname: '管理员' })
  })

  it('请求途中静默续期换了令牌：仍是同一次登录，用户信息照常写入', async () => {
    startSession('T1', true)
    taro.request
      .mockResolvedValueOnce(expired)
      .mockResolvedValueOnce({ statusCode: 200, data: { access_token: 'T2', miniapp_bound: true } })
      .mockResolvedValueOnce({ statusCode: 200, data: { id: 1, nickname: '管理员' } })
    await loadUser()
    expect(useSession.getState().token).toBe('T2')
    expect(useSession.getState().user).toEqual({ id: 1, nickname: '管理员' })
  })

  it('取回来时已经换了一次登录：不写', async () => {
    startSession('T1', true)
    taro.request.mockImplementationOnce(async () => {
      clearSession()
      startSession('U1', true)
      return { statusCode: 200, data: { id: 1, nickname: '上一个用户' } }
    })
    await loadUser()
    expect(useSession.getState().token).toBe('U1')
    expect(useSession.getState().user).toBeNull()
  })
})

describe('avatarSrc', () => {
  it('后端相对路径补上后端地址，完整地址和空值原样返回', () => {
    expect(avatarSrc(undefined)).toBe('')
    expect(avatarSrc('https://cdn.example.com/a.png')).toBe('https://cdn.example.com/a.png')
    expect(avatarSrc('/admin/v1/avatars/1/a.png')).toBe('/admin/v1/avatars/1/a.png')
  })
})
