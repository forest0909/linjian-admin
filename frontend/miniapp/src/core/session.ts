import Taro from '@tarojs/taro'
import { create } from 'zustand'
import { isRenewRejected, TEXT_SESSION_EXPIRED } from './errors'

/**
 * 会话：令牌存取和静默续期的单飞。
 *
 * 两个用来判断「响应是不是晚到了」的值：
 * - token：续期成功会换。请求用的 token 和当前不同，说明别的请求已经续期成功。
 * - loginId：每次登录、每次清状态都加一，续期不变。请求的 loginId 和当前不同，
 *   说明发出它之后用户退出过或重新登录过，这个响应一律丢弃。
 */

export const LOGIN_PAGE = '/pages/login/index'
export const HOME_PAGE = '/pages/home/index'

const STORAGE_KEY = 'linjian.miniapp.session'

/** `GET /admin/v1/me` 里页面用到的字段。 */
export interface SessionUser {
  id?: number
  username?: string
  nickname?: string
  realname?: string
  avatar?: string
  mobile?: string
}

interface SessionState {
  token: string | null
  /** 登录响应的 miniapp_bound：这个微信已绑定本用户，令牌过期后可以静默续期。 */
  bound: boolean
  loginId: number
  user: SessionUser | null
}

export interface SessionSnapshot {
  token: string | null
  loginId: number
}

function readStorage(): { token: string | null; bound: boolean } {
  try {
    const saved = Taro.getStorageSync(STORAGE_KEY)
    if (saved && typeof saved.token === 'string' && saved.token) {
      return { token: saved.token, bound: saved.bound === true }
    }
  } catch (err) {
    // 本地存储读不出来就当没登录，用户重新登录即可。
    console.warn('[session] 读取本地会话失败', err)
  }
  return { token: null, bound: false }
}

function writeStorage(token: string | null, bound: boolean): void {
  try {
    if (token) {
      Taro.setStorageSync(STORAGE_KEY, { token, bound })
    } else {
      Taro.removeStorageSync(STORAGE_KEY)
    }
  } catch (err) {
    // 写不进去只影响下次启动是否还在登录态，本次会话照常。
    console.warn('[session] 写入本地会话失败', err)
  }
}

export const useSession = create<SessionState>(() => ({
  ...readStorage(),
  loginId: 1,
  user: null,
}))

export function snapshot(): SessionSnapshot {
  const { token, loginId } = useSession.getState()
  return { token, loginId }
}

export function isBound(): boolean {
  return useSession.getState().bound
}

/** 这个快照是不是还对应当前会话（同一次登录，令牌也没换）。 */
export function isCurrent(snap: SessionSnapshot): boolean {
  const cur = snapshot()
  return cur.token !== null && cur.loginId === snap.loginId && cur.token === snap.token
}

/** 登录成功：开始一次新的登录。 */
export function startSession(token: string, bound: boolean): void {
  useSession.setState((s) => ({ token, bound, user: null, loginId: s.loginId + 1 }))
  writeStorage(token, bound)
}

export function setUser(user: SessionUser | null): void {
  useSession.setState({ user })
}

/** 清状态：令牌、bound、用户信息都清掉，这次登录到此结束。 */
export function clearSession(): void {
  useSession.setState((s) => ({ token: null, bound: false, user: null, loginId: s.loginId + 1 }))
  writeStorage(null, false)
}

export function toLoginPage(): void {
  Taro.reLaunch({ url: LOGIN_PAGE }).catch((err) => {
    console.error('[session] 跳转登录页失败', err)
  })
}

/**
 * 会话失效：清状态并回登录页。只在快照仍是当前会话时才动手，
 * 所以并发的多个失效响应只会清一次、跳一次，也不会误清之后新登录的会话。
 */
export function expireSession(snap: SessionSnapshot): boolean {
  if (!isCurrent(snap)) {
    return false
  }
  clearSession()
  Taro.showToast({ title: TEXT_SESSION_EXPIRED, icon: 'none' })
  toLoginPage()
  return true
}

/**
 * 续期结果。
 * - renewed：新令牌已保存，可以重放。
 * - rejected：被明确拒绝，已清状态并回登录页。
 * - failed：临时故障，状态没动，下一次请求会再试。
 * - stale：续期途中会话变了（退出或重新登录），结果已丢弃。
 */
export type RenewOutcome = 'renewed' | 'rejected' | 'failed' | 'stale'

let renewing: Promise<RenewOutcome> | null = null

/**
 * 静默续期，单飞：同一时间只有一次，期间再调用拿到的是同一个 Promise。
 * exchange 负责向后端换新令牌（取 wx.login code 并调续期接口），由请求封装传入。
 */
export function renewSession(exchange: () => Promise<string>): Promise<RenewOutcome> {
  if (renewing) {
    return renewing
  }
  const start = snapshot()
  const run = async (): Promise<RenewOutcome> => {
    try {
      const token = await exchange()
      if (!isCurrent(start)) {
        return 'stale'
      }
      // 续期不算新登录：loginId 不变，只换令牌。续期成功说明绑定还在，bound 保持 true。
      useSession.setState({ token })
      writeStorage(token, true)
      return 'renewed'
    } catch (err) {
      if (!isCurrent(start)) {
        console.warn('[session] 续期失败，但会话已变，忽略', err)
        return 'stale'
      }
      if (isRenewRejected(err)) {
        console.warn('[session] 续期被拒绝，回登录页', err)
        expireSession(start)
        return 'rejected'
      }
      console.error('[session] 续期遇到临时故障', err)
      return 'failed'
    }
  }
  renewing = run().finally(() => {
    renewing = null
  })
  return renewing
}
