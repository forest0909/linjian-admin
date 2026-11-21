import { View, Text } from '@tarojs/components'
import { Button, Cell } from '@nutui/nutui-react-taro'
import Taro from '@tarojs/taro'
import './index.scss'

export default function Home() {
  return (
    <View className='home'>
      <Text className='home__title'>林间 Admin</Text>
      <Cell title='构建平台' extra={process.env.TARO_ENV} />
      <Button type='primary' block onClick={() => Taro.showToast({ title: '组件库可用', icon: 'none' })}>
        点一下
      </Button>
    </View>
  )
}
