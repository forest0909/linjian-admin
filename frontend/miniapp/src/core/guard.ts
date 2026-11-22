import { useDidShow } from '@tarojs/taro'
import { loadUser } from '@/api/profile'
import { snapshot, toLoginPage, useSession } from './session'

/**
 * 需要登录的页面在组件里调一次。
 * 页面显示时：没有令牌就回登录页；有令牌但还没有用户信息就取一次。
 */
export function useRequireLogin(): void {
  useDidShow(() => {
    if (!snapshot().token) {
      toLoginPage()
      return
    }
    if (!useSession.getState().user) {
      loadUser().catch((err) => {
        console.error('[guard] 获取用户信息失败', err)
      })
    }
  })
}
