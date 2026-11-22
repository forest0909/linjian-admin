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
import { fakeJwt } from '../test-utils'
import { avatarSrc, changePassword, loadUser, updateAvatar, type PickResult } from './profile'

// 形状和后端 access token 一样的假 JWT。T* 属于用户 1，U1 属于用户 2。
const T1 = fakeJwt(1, 't1')
const T2 = fakeJwt(1, 't2')
const U1 = fakeJwt(2, 'u1')

const expired = { statusCode: 401, data: { code: 401, reason: 'TOKEN_EXPIRED' } }

beforeEach(() => {
  taro.request.mockReset()
  taro.login.mockReset()
  taro.login.mockResolvedValue({ code: 'wx-code', errMsg: 'login:ok' })
  taro.reLaunch.mockReset()
  taro.reLaunch.mockResolvedValue({})
  taro.showToast.mockReset()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  clearSession()
})

describe('loadUser', () => {
  it('取到用户信息后写进会话', async () => {
    startSession(T1, true)
    taro.request.mockResolvedValueOnce({ statusCode: 200, data: { id: 1, nickname: '管理员' } })
    await loadUser()
    expect(useSession.getState().user).toEqual({ id: 1, nickname: '管理员' })
  })

  it('请求途中静默续期换了令牌：仍是同一次登录，用户信息照常写入', async () => {
    startSession(T1, true)
    taro.request
      .mockResolvedValueOnce(expired)
      .mockResolvedValueOnce({ statusCode: 200, data: { access_token: T2, miniapp_bound: true } })
      .mockResolvedValueOnce({ statusCode: 200, data: { id: 1, nickname: '管理员' } })
    await loadUser()
    expect(useSession.getState().token).toBe(T2)
    expect(useSession.getState().user).toEqual({ id: 1, nickname: '管理员' })
  })

  it('取回来时已经换了一次登录：不写', async () => {
    startSession(T1, true)
    taro.request.mockImplementationOnce(async () => {
      clearSession()
      startSession(U1, true)
      return { statusCode: 200, data: { id: 1, nickname: '上一个用户' } }
    })
    await loadUser()
    expect(useSession.getState().token).toBe(U1)
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

/** 一个由用例控制何时返回的异步步骤。 */
function deferred<T>() {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((res) => (resolve = res))
  return { promise, resolve }
}
const okEmpty = { statusCode: 200, data: {} }

describe('changePassword', () => {
  it('成功：口令加密后发出，清状态并回登录页', async () => {
    startSession(T1, true)
    taro.request.mockResolvedValueOnce(okEmpty)
    await expect(changePassword('old-pass', 'new-pass')).resolves.toBe('changed')
    const req = taro.request.mock.calls[0][0]
    expect(req.url).toBe('/admin/v1/me/password')
    expect(JSON.stringify(req.data)).not.toContain('old-pass')
    expect(JSON.stringify(req.data)).not.toContain('new-pass')
    expect(useSession.getState().token).toBeNull()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })

  it('失败：抛出错误，会话不动', async () => {
    startSession(T1, true)
    taro.request.mockResolvedValueOnce({ statusCode: 400, data: { reason: 'INVALID_PASSWORD' } })
    await expect(changePassword('a', 'b')).rejects.toMatchObject({ reason: 'INVALID_PASSWORD' })
    expect(useSession.getState().token).toBe(T1)
    expect(taro.reLaunch).not.toHaveBeenCalled()
  })

  it('改密请求还没回来就换了账号：成功回包不清新会话、不跳转', async () => {
    startSession(T1, true)
    const server = deferred<typeof okEmpty>()
    taro.request.mockReturnValueOnce(server.promise)
    const done = changePassword('a', 'b')
    clearSession()
    startSession(U1, true)
    server.resolve(okEmpty)
    await expect(done).resolves.toBe('discarded')
    expect(useSession.getState().token).toBe(U1)
    expect(taro.reLaunch).not.toHaveBeenCalled()
    expect(taro.showToast).not.toHaveBeenCalled()
  })

  it('改密途中静默续期换了令牌：仍是同一次登录，照常清状态回登录页', async () => {
    startSession(T1, true)
    taro.request
      .mockResolvedValueOnce(expired)
      .mockResolvedValueOnce({ statusCode: 200, data: { access_token: T2 } })
      .mockResolvedValueOnce(okEmpty)
    await expect(changePassword('a', 'b')).resolves.toBe('changed')
    expect(useSession.getState().token).toBeNull()
    expect(taro.reLaunch).toHaveBeenCalledTimes(1)
  })
})

describe('updateAvatar', () => {
  const picked: PickResult = { kind: 'picked', base64: 'QUJD' }

  it('选图 → 上传 → 刷新用户信息', async () => {
    startSession(T1, true)
    taro.request
      .mockResolvedValueOnce({ statusCode: 200, data: { url: '/admin/v1/avatars/1/a.png' } })
      .mockResolvedValueOnce({ statusCode: 200, data: { id: 1, avatar: '/admin/v1/avatars/1/a.png' } })
    await expect(updateAvatar(async () => picked)).resolves.toBe('updated')
    expect(taro.request.mock.calls[0][0]).toMatchObject({ url: '/admin/v1/me/avatar', data: { imageBase64: 'QUJD' } })
    expect(useSession.getState().user).toEqual({ id: 1, avatar: '/admin/v1/avatars/1/a.png' })
  })

  it.each([{ kind: 'cancelled' }, { kind: 'too-large' }] as PickResult[])('选图结果是 $kind：不上传', async (result) => {
    startSession(T1, true)
    await expect(updateAvatar(async () => result)).resolves.toBe(result.kind)
    expect(taro.request).not.toHaveBeenCalled()
  })

  it('选图期间换了账号：不上传（否则图会传到新账号上）', async () => {
    startSession(T1, true)
    const picker = deferred<PickResult>()
    const done = updateAvatar(() => picker.promise)
    clearSession()
    startSession(U1, true)
    picker.resolve(picked)
    await expect(done).resolves.toBe('discarded')
    expect(taro.request).not.toHaveBeenCalled()
    expect(useSession.getState().token).toBe(U1)
  })

  it('选图期间退出了：不上传', async () => {
    startSession(T1, true)
    const picker = deferred<PickResult>()
    const done = updateAvatar(() => picker.promise)
    clearSession()
    picker.resolve(picked)
    await expect(done).resolves.toBe('discarded')
    expect(taro.request).not.toHaveBeenCalled()
  })

  it('上传期间换了账号：不刷新用户信息，新账号的资料不被覆盖', async () => {
    startSession(T1, true)
    const server = deferred<{ statusCode: number; data: unknown }>()
    taro.request.mockReturnValueOnce(server.promise)
    const done = updateAvatar(async () => picked)
    await Promise.resolve()
    await Promise.resolve()
    clearSession()
    startSession(U1, true)
    server.resolve({ statusCode: 200, data: { url: '/x.png' } })
    await expect(done).resolves.toBe('discarded')
    expect(taro.request).toHaveBeenCalledTimes(1)
    expect(useSession.getState().user).toBeNull()
  })

  it('选图期间静默续期换了令牌：仍是同一次登录，照常上传', async () => {
    startSession(T1, true)
    const picker = deferred<PickResult>()
    const done = updateAvatar(() => picker.promise)
    // 别的请求触发的续期在这期间完成
    useSession.setState({ token: T2 })
    taro.request
      .mockResolvedValueOnce({ statusCode: 200, data: { url: '/x.png' } })
      .mockResolvedValueOnce({ statusCode: 200, data: { id: 1 } })
    picker.resolve(picked)
    await expect(done).resolves.toBe('updated')
    expect(taro.request.mock.calls[0][0].header.Authorization).toBe(`Bearer ${T2}`)
  })
})
