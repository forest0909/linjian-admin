import CryptoJS from 'crypto-js'
import { describe, expect, it } from 'vitest'
import { encryptPassword, tokenUid } from './crypto'
import { fakeJwt } from '../test-utils'

describe('encryptPassword', () => {
  it('AES-128-CBC，key 和 iv 相同，输出 base64，能按同一规则解回原文', () => {
    const key = CryptoJS.enc.Utf8.parse(process.env.TARO_APP_AES_KEY as string)
    const cipher = encryptPassword('Abcd@1234')
    expect(cipher).toMatch(/^[A-Za-z0-9+/]+=*$/)
    const plain = CryptoJS.AES.decrypt(cipher, key, { iv: key, mode: CryptoJS.mode.CBC, padding: CryptoJS.pad.Pkcs7 })
    expect(plain.toString(CryptoJS.enc.Utf8)).toBe('Abcd@1234')
  })

  it('和管理端同一 key 下的密文一致', () => {
    // 期望值由 Node 自带的 crypto（aes-128-cbc，key = iv = f51d66a73d8a0927）独立算出
    expect(encryptPassword('Abcd@1234')).toBe('5iATn4qtWi3ej5hNxxBcEA==')
  })
})

describe('tokenUid', () => {
  it('从 JWT 载荷里取 uid', () => {
    expect(tokenUid(fakeJwt(42, 'a'))).toBe(42)
    expect(tokenUid(fakeJwt(42, 'b'))).toBe(42)
  })

  it('取不出来返回 null：没有 uid、uid 不是数字、不是 JWT、载荷不是 JSON', () => {
    expect(tokenUid(fakeJwt(null, 'a'))).toBeNull()
    const encode = (v: unknown) => Buffer.from(JSON.stringify(v)).toString('base64url')
    expect(tokenUid(`${encode({})}.${encode({ uid: '42' })}.sig`)).toBeNull()
    expect(tokenUid('opaque-token')).toBeNull()
    expect(tokenUid('a.%%%.c')).toBeNull()
    expect(tokenUid(`a.${Buffer.from('not json').toString('base64url')}.c`)).toBeNull()
    expect(tokenUid('')).toBeNull()
  })
})
