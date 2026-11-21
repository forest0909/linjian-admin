import { encryptPassword } from '@/core/crypto'
import { request } from '@/core/request'
import { isSameLogin, setUser, snapshot, type SessionUser } from '@/core/session'

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

/** 修改密码。新旧口令都按登录同规加密后传输。 */
export function changePassword(oldPassword: string, newPassword: string): Promise<void> {
  return request<void>({
    method: 'POST',
    url: '/admin/v1/me/password',
    data: { oldPassword: encryptPassword(oldPassword), newPassword: encryptPassword(newPassword) },
  })
}

/** 上传头像。imageBase64 是不带 `data:` 前缀的纯 base64。返回新头像地址。 */
export async function uploadAvatar(imageBase64: string): Promise<string> {
  const res = await request<{ url?: string }>({ method: 'POST', url: '/admin/v1/me/avatar', data: { imageBase64 } })
  return res?.url ?? ''
}

/** 头像地址是后端的相对路径（/admin/v1/avatars/...），小程序里要补上后端地址。 */
export function avatarSrc(avatar: string | undefined): string {
  if (!avatar) {
    return ''
  }
  return avatar.startsWith('/') ? (process.env.TARO_APP_API_BASE || '') + avatar : avatar
}
