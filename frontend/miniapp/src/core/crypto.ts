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

/**
 * 从 access token（JWT）的载荷里取用户 ID（`uid`）。取不出来返回 null。
 * 只用来在客户端核对「续期前后是不是同一个账号」，不验签，鉴权仍由服务端做。
 */
export function tokenUid(token: string): number | null {
  const payload = token.split('.')[1]
  if (!payload) {
    return null
  }
  try {
    const claims = JSON.parse(CryptoJS.enc.Base64url.parse(payload).toString(CryptoJS.enc.Utf8))
    const uid = claims?.uid
    return typeof uid === 'number' && Number.isFinite(uid) ? uid : null
  } catch {
    // 不是合法的 JWT：按「取不出 uid」处理，由调用方决定怎么办。错误内容里可能带令牌片段，不打日志。
    return null
  }
}
