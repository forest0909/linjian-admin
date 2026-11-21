import CryptoJS from 'crypto-js'

/**
 * 口令传输加密，和管理端同规：AES-128-CBC，key 和 iv 都是 TARO_APP_AES_KEY，PKCS7，输出 base64。
 * 登录、改密的口令字段必须先过这个函数，后端按密文解密。
 */
export function encryptPassword(password: string): string {
  const key = process.env.TARO_APP_AES_KEY
  if (!key) {
    throw new Error('TARO_APP_AES_KEY 未配置')
  }
  const parsed = CryptoJS.enc.Utf8.parse(key)
  return CryptoJS.AES.encrypt(password, parsed, {
    iv: parsed,
    mode: CryptoJS.mode.CBC,
    padding: CryptoJS.pad.Pkcs7,
  }).toString()
}
