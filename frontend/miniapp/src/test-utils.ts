/**
 * 单测用的辅助函数。不会被页面引用，不进构建产物。
 */

/**
 * 造一个形状像后端 access token 的 JWT：载荷里有 `uid`，签名是假的。
 * tag 用来让同一个用户的两个令牌不相同。uid 传 null 表示载荷里没有 uid。
 */
export function fakeJwt(uid: number | null, tag: string): string {
  const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
  const payload = uid === null ? { jti: tag } : { uid, jti: tag }
  return `${encode({ alg: 'HS256', typ: 'JWT' })}.${encode(payload)}.sig-${tag}`
}
