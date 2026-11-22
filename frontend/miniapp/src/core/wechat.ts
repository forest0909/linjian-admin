import Taro from '@tarojs/taro'

/**
 * 微信能力的封装：wx.login 和手机号授权。
 *
 * mock：TARO_APP_WECHAT_MOCK=true 且不是生产构建时，不调微信，返回后端 mock 构建认识的固定 code。
 * mock 分支都直接写 `process.env.NODE_ENV !== 'production'` 这个条件：构建时它被替换成常量，
 * 生产包里整段分支被删掉（跨模块引用 WECHAT_MOCK 删不掉）。config/index.ts 还会让开着 mock 的生产构建直接失败。
 */

export const IS_WEAPP = process.env.TARO_ENV === 'weapp'

export const WECHAT_MOCK = process.env.NODE_ENV !== 'production' && process.env.TARO_APP_WECHAT_MOCK === 'true'

/** 能不能拿到微信 code：小程序里可以；H5 只有开了 mock 才行。决定一键登录和静默续期是否可用。 */
export const WECHAT_AVAILABLE = IS_WEAPP || WECHAT_MOCK

/** 取一个新的 wx.login code。H5（没开 mock）下没有微信，调用会抛错，调用前先看 WECHAT_AVAILABLE。 */
export async function getLoginCode(): Promise<string> {
  if (process.env.NODE_ENV !== 'production' && WECHAT_MOCK) {
    return 'mock_openid_dev'
  }
  if (!IS_WEAPP) {
    throw new Error('当前平台没有 wx.login')
  }
  const res = await Taro.login()
  if (!res.code) {
    throw new Error(`wx.login 没有返回 code：${res.errMsg}`)
  }
  return res.code
}

/**
 * 从 `<Button openType="getPhoneNumber">` 的回调里取手机号 code。
 * 用户拒绝授权时没有 code，返回 null。
 */
export function phoneCodeFromEvent(detail: { code?: string; errMsg?: string } | undefined): string | null {
  return detail?.code || null
}

/** mock 下的手机号 code：后端 mock 把 `mock_phone_` 后面的部分当作手机号。 */
export function mockPhoneCode(mobile: string): string {
  return process.env.NODE_ENV !== 'production' ? `mock_phone_${mobile}` : ''
}
