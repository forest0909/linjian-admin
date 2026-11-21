import CryptoJS from 'crypto-js'
import { describe, expect, it } from 'vitest'
import { encryptPassword } from './crypto'

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
