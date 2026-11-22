import { encryptPassword } from '@/core/crypto'
import { request } from '@/core/request'
import { clearSession, isSameLogin, setUser, snapshot, toLoginPage, type SessionUser } from '@/core/session'

export function getMe(): Promise<SessionUser> {
  return request<SessionUser>({ method: 'GET', url: '/admin/v1/me' })
}

/**
 * 取当前用户信息并放进会话。取回来时已经不是同一次登录（退出或换号）就不写。
 * 请求途中静默续期换了令牌仍是同一次登录，照常写入。
 */
export async function loadUser(): Promise<void> {
  const snap = snapshot()
  const user = await getMe()
  if (isSameLogin(snap)) {
    setUser(user)
  }
}

/**
 * 下面两个动作都是「开始时记下这次登录，异步步骤回来后先核对，不是同一次登录就静默丢弃」。
 * 途中静默续期换了令牌仍算同一次登录。返回 'discarded' 时调用方什么都不要做（不提示、不跳转）。
 */

/**
 * 修改密码。新旧口令都按登录同规加密后传输。
 * 成功后服务端会让这个账号的小程序会话和微信绑定全部失效，所以这里直接清状态回登录页。
 * 失败时抛出请求封装的错误。
 */
export async function changePassword(oldPassword: string, newPassword: string): Promise<'changed' | 'discarded'> {
  const snap = snapshot()
  await request<void>({
    method: 'POST',
    url: '/admin/v1/me/password',
    data: { oldPassword: encryptPassword(oldPassword), newPassword: encryptPassword(newPassword) },
  })
  if (!isSameLogin(snap)) {
    return 'discarded'
  }
  clearSession()
  toLoginPage()
  return 'changed'
}

/** 选图的结果。base64 不带 `data:` 前缀。 */
export type PickResult = { kind: 'picked'; base64: string } | { kind: 'cancelled' } | { kind: 'too-large' }

/**
 * 更换头像：选图 → 上传 → 刷新用户信息。pick 由页面传入（两端选图的写法不同）。
 * 选图要等用户操作，可能很久；回来时已经不是同一次登录就不上传。
 */
export async function updateAvatar(
  pick: () => Promise<PickResult>,
): Promise<'updated' | 'cancelled' | 'too-large' | 'discarded'> {
  const snap = snapshot()
  const image = await pick()
  if (!isSameLogin(snap)) {
    return 'discarded'
  }
  if (image.kind !== 'picked') {
    return image.kind
  }
  await request<{ url?: string }>({ method: 'POST', url: '/admin/v1/me/avatar', data: { imageBase64: image.base64 } })
  if (!isSameLogin(snap)) {
    return 'discarded'
  }
  await loadUser()
  return isSameLogin(snap) ? 'updated' : 'discarded'
}

/** 头像地址是后端的相对路径（/admin/v1/avatars/...），小程序里要补上后端地址。 */
export function avatarSrc(avatar: string | undefined): string {
  if (!avatar) {
    return ''
  }
  return avatar.startsWith('/') ? (process.env.TARO_APP_API_BASE || '') + avatar : avatar
}
