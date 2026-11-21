export default defineAppConfig({
  // 第一个是启动页。首页发现没登录会转到登录页。
  pages: ['pages/home/index', 'pages/mine/index', 'pages/login/index'],
  window: {
    backgroundTextStyle: 'light',
    navigationBarBackgroundColor: '#ffffff',
    navigationBarTitleText: '林间 Admin',
    navigationBarTextStyle: 'black',
  },
})
