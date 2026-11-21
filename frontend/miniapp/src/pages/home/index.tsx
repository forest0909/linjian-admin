import { View, Text } from '@tarojs/components'
import { Avatar } from '@nutui/nutui-react-taro'
import Taro from '@tarojs/taro'
import { avatarSrc } from '@/api/profile'
import { useRequireLogin } from '@/core/guard'
import { MINE_PAGE, useSession } from '@/core/session'
import './index.scss'

export default function Home() {
  useRequireLogin()
  const user = useSession((s) => s.user)
  const name = user?.nickname || user?.realname || user?.username || ''

  function openMine() {
    Taro.navigateTo({ url: MINE_PAGE }).catch((err) => {
      console.error('[home] 打开「我的」失败', err)
    })
  }

  return (
    <View className='home'>
      <View className='home__hello' onClick={openMine}>
        <View className='home__hello-text'>
          <Text className='home__greeting'>{name ? `你好，${name}` : '你好'}</Text>
          <Text className='home__sub'>欢迎使用林间 Admin</Text>
        </View>
        <Avatar size='48' src={avatarSrc(user?.avatar)} background='#006be6' color='#ffffff'>
          {name.slice(0, 1)}
        </Avatar>
      </View>
      <View className='home__card'>
        <Text className='home__card-title'>这里是首页占位</Text>
        <Text className='home__card-text'>业务页面从这里开始加。点右上角的头像进入「我的」。</Text>
      </View>
    </View>
  )
}
