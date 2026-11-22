import Taro from '@tarojs/taro'
import { encryptPassword } from '@/core/crypto'
import { request } from '@/core/request'
import { clearSession, snapshot, startSession, toLoginPage } from '@/core/session'
import { loadUser } from './profile'

/** `GET /admin/v1/captcha` 的响应。imageBase64 是可以直接放进 Image.src 的 data URL。 */
export interface Captcha {
  captchaId: string
  imageBase64: string
}

/** 三个登录接口共用的响应。expires_in 是 int64，线上是字符串，这里用不到。 */
interface LoginResponse {
  access_token?: string
  miniapp_bound?: boolean
}

export function fetchCaptcha(): Promise<Captcha> {
  return request<Captcha>({ method: 'GET', url: '/admin/v1/captcha', auth: false })
}

/** 登录成功：保存令牌和 bound，再取一次用户信息。 */
async function enter(res: LoginResponse): Promise<void> {
  if (!res || !res.access_token) {
    throw new Error('登录响应里没有 access_token')
  }
  startSession(res.access_token, res.miniapp_bound === true)
  try {
    await loadUser()
  } catch (err) {
    // 登录本身已成功。用户信息没取到不挡进首页，首页显示时会再取一次。
    console.error('[auth] 登录后获取用户信息失败', err)
  }
}

export interface PasswordLoginParams {
  username: string
  password: string
  captchaId: string
  captchaValue: string
  /** wx.login 的 code。带上时后端顺带把这个微信绑定到账号；H5 下不带。 */
  code?: string
}

/** 账号密码登录。请求体是扁平 JSON，验证码走请求头。 */
export async function loginByPassword(params: PasswordLoginParams): Promise<void> {
  const data: Record<string, string> = {
    grant_type: 'password',
    client_type: 'app',
    username: params.username,
    password: encryptPassword(params.password),
  }
  if (params.code) {
    data.code = params.code
  }
  const res = await request<LoginResponse>({
    method: 'POST',
    url: '/admin/v1/login',
    data,
    headers: { 'X-Captcha-Id': params.captchaId, 'X-Captcha-Value': params.captchaValue },
    auth: false,
  })
  await enter(res)
}

/** 手机号一键登录。两个 code 都交给后端向微信兑换。 */
export async function loginByPhone(code: string, phoneCode: string): Promise<void> {
  const res = await request<LoginResponse>({
    method: 'POST',
    url: '/admin/v1/miniapp/login/phone',
    data: { code, phoneCode },
    auth: false,
  })
  await enter(res)
}

/**
 * 退出：先记下令牌、清本地状态并回登录页，再用记下的令牌通知后端。
 * 接口失败只提示一句。
 *
 * 跳转在发请求之前做，接口回来之后不再跳转：退出接口还没返回时用户可能已经重新登录，
 * 晚到的回包不能把新会话带回登录页。失败提示也只在没有重新登录时才弹。
 */
export async function logout(): Promise<void> {
  const { token } = snapshot()
  clearSession()
  const after = snapshot()
  toLoginPage()
  if (!token) {
    return
  }
  try {
    await request({ method: 'POST', url: '/admin/v1/miniapp/logout', data: {}, token, silent: true })
  } catch (err) {
    console.error('[auth] 退出接口失败', err)
    if (snapshot().loginId === after.loginId) {
      Taro.showToast({ title: '退出未完全成功', icon: 'none' })
    }
  }
}
