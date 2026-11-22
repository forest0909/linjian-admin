export default defineAppConfig({
  // 第一个是启动页。首页发现没登录会转到登录页。
  pages: ['pages/home/index', 'pages/mine/index', 'pages/login/index'],
  window: {
    backgroundTextStyle: 'light',
    navigationBarBackgroundColor: '#ffffff',
    navigationBarTitleText: '林间 Admin',
    navigationBarTextStyle: 'black',
  },
  // 登录页不在 list 里，所以不显示 tabBar。
  // 图标是 81x81 的 PNG，由 scripts/gen-tabbar-icons.mjs 从同目录的 SVG 生成；
  // 这里的 color / selectedColor 改了，脚本里的颜色要跟着改并重新生成。
  tabBar: {
    color: '#8a9099',
    selectedColor: '#006be6',
    backgroundColor: '#ffffff',
    borderStyle: 'black',
    list: [
      {
        pagePath: 'pages/home/index',
        text: '首页',
        iconPath: 'assets/tabbar/home.png',
        selectedIconPath: 'assets/tabbar/home-active.png',
      },
      {
        pagePath: 'pages/mine/index',
        text: '我的',
        iconPath: 'assets/tabbar/mine.png',
        selectedIconPath: 'assets/tabbar/mine-active.png',
      },
    ],
  },
})
