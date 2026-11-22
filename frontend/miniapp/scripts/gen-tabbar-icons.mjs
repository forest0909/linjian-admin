#!/usr/bin/env node
/**
 * 从 src/assets/tabbar/*.svg 生成 tabBar 用的 PNG（81x81，透明底）。
 *
 * 小程序的 tabBar 只收 PNG / JPG，不能直接用 SVG。每个 SVG 用 currentColor 画，
 * 这里按未选中、选中两种颜色各出一张：<name>.png 和 <name>-active.png。
 * 颜色要和 src/app.config.ts 里 tabBar 的 color / selectedColor 一致。
 *
 * 用本机的 Chrome 无头模式栅格化，不需要装任何 npm 依赖。生成的 PNG 提交进版本库，
 * 构建和 CI 不会跑这个脚本；只有改了图标或颜色才需要重新生成：
 *
 *   node scripts/gen-tabbar-icons.mjs
 *   CHROME_PATH=/path/to/chrome node scripts/gen-tabbar-icons.mjs
 */
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const SIZE = 81
const COLORS = { '': '#8a9099', '-active': '#006be6' }

const dir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../src/assets/tabbar')
const chrome =
  process.env.CHROME_PATH ||
  [
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/usr/bin/google-chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  ].find((p) => fs.existsSync(p))

if (!chrome) {
  console.error('找不到 Chrome，用 CHROME_PATH 指定可执行文件的路径')
  process.exit(1)
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'tabbar-icons-'))
try {
  for (const file of fs.readdirSync(dir).filter((f) => f.endsWith('.svg'))) {
    const name = path.basename(file, '.svg')
    const svg = fs.readFileSync(path.join(dir, file), 'utf8')
    for (const [suffix, color] of Object.entries(COLORS)) {
      const html = path.join(tmp, `${name}${suffix}.html`)
      fs.writeFileSync(
        html,
        `<!doctype html><style>html,body{margin:0;background:transparent}svg{display:block;width:${SIZE}px;height:${SIZE}px;color:${color}}</style>${svg}`,
      )
      const out = path.join(dir, `${name}${suffix}.png`)
      execFileSync(
        chrome,
        [
          '--headless=new',
          '--disable-gpu',
          '--hide-scrollbars',
          '--force-device-scale-factor=1',
          '--default-background-color=00000000',
          `--window-size=${SIZE},${SIZE}`,
          `--user-data-dir=${path.join(tmp, 'profile')}`,
          `--screenshot=${out}`,
          pathToFileURL(html).href,
        ],
        { stdio: 'ignore' },
      )
      console.log(`${path.relative(process.cwd(), out)}  ${color}`)
    }
  }
} finally {
  fs.rmSync(tmp, { recursive: true, force: true })
}
