import { View, Text } from '@tarojs/components'
import { useRequireLogin } from '@/core/guard'
import { useSession } from '@/core/session'
import './index.scss'

export default function Home() {
  useRequireLogin()
  const user = useSession((s) => s.user)
  const name = user?.nickname || user?.realname || user?.username || ''

  return (
    <View className='home'>
      <View className='home__hello'>
        <Text className='home__greeting'>{name ? `你好，${name}` : '你好'}</Text>
        <Text className='home__sub'>欢迎使用林间 Admin</Text>
      </View>
      <View className='home__card'>
        <Text className='home__card-title'>这里是首页占位</Text>
        <Text className='home__card-text'>业务页面从这里开始加。加页面的步骤见 frontend/miniapp/AGENTS.md。</Text>
      </View>
    </View>
  )
}
