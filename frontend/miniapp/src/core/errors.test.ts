import { describe, expect, it } from 'vitest'
import { ApiError, errorText, isRenewRejected, isTokenExpired } from './errors'

function apiError(status: number, reason: string) {
  return new ApiError({ status, reason, method: 'GET', path: '/x' })
}

describe('errors', () => {
  it('六个小程序 reason 都有中文文案', () => {
    for (const reason of [
      'MINIAPP_CODE_INVALID',
      'MINIAPP_PHONE_NOT_REGISTERED',
      'MINIAPP_NOT_BOUND',
      'MINIAPP_PHONE_AMBIGUOUS',
      'MINIAPP_BOUND_TO_OTHER',
      'MINIAPP_UNAVAILABLE',
    ]) {
      expect(errorText(apiError(400, reason))).not.toBe('请求参数错误')
    }
    expect(errorText(apiError(403, 'MINIAPP_PHONE_NOT_REGISTERED'))).toBe('该手机号未开通账号，请联系管理员')
  })

  it('没有对应 reason 时按状态码，再没有就用通用文案', () => {
    expect(errorText(apiError(404, 'SOMETHING_NEW'))).toBe('请求的资源不存在')
    expect(errorText(apiError(418, ''))).toBe('请求失败，请稍后重试')
    expect(errorText(new Error('x'))).toBe('请求失败，请稍后重试')
  })

  it('只有 401 + TOKEN_EXPIRED 算令牌失效', () => {
    expect(isTokenExpired(apiError(401, 'TOKEN_EXPIRED'))).toBe(true)
    expect(isTokenExpired(apiError(401, 'UNAUTHORIZED'))).toBe(false)
    expect(isTokenExpired(apiError(400, 'TOKEN_EXPIRED'))).toBe(false)
  })

  it('续期被明确拒绝的三种 reason', () => {
    expect(isRenewRejected(apiError(403, 'MINIAPP_NOT_BOUND'))).toBe(true)
    expect(isRenewRejected(apiError(400, 'MINIAPP_CODE_INVALID'))).toBe(true)
    expect(isRenewRejected(apiError(403, 'FORBIDDEN'))).toBe(true)
    expect(isRenewRejected(apiError(503, 'MINIAPP_UNAVAILABLE'))).toBe(false)
    expect(isRenewRejected(apiError(429, 'TOO_MANY_REQUESTS'))).toBe(false)
    expect(isRenewRejected(apiError(409, 'CONFLICT'))).toBe(false)
  })
})
