import { afterEach, describe, expect, it, vi } from 'vitest'

vi.hoisted(() => {
  process.env.TARO_ENV = 'h5'
})

const taro = vi.hoisted(() => ({ chooseImage: vi.fn(), getFileSystemManager: vi.fn() }))
vi.mock('@tarojs/taro', () => ({ default: taro }))

import { pickImage } from './avatar'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('pickImage（H5）', () => {
  it('超过上限：先按 Blob.size 拒绝，不生成 base64', async () => {
    const reader = vi.fn()
    vi.stubGlobal('FileReader', reader)
    taro.chooseImage.mockResolvedValue({ tempFiles: [{ path: 'blob:x', size: 0, originalFileObj: { size: 11 } }] })
    await expect(pickImage(10)).resolves.toEqual({ kind: 'too-large' })
    expect(reader).not.toHaveBeenCalled()
    expect(taro.getFileSystemManager).not.toHaveBeenCalled()
  })

  it('没超过上限：用 FileReader 读成不带前缀的 base64', async () => {
    class FakeReader {
      result: string | null = null
      onload: (() => void) | null = null
      onerror: (() => void) | null = null
      readAsDataURL() {
        this.result = 'data:image/png;base64,QUJD'
        this.onload?.()
      }
    }
    vi.stubGlobal('FileReader', FakeReader)
    taro.chooseImage.mockResolvedValue({ tempFiles: [{ path: 'blob:x', size: 0, originalFileObj: { size: 10 } }] })
    await expect(pickImage(10)).resolves.toEqual({ kind: 'picked', base64: 'QUJD' })
  })
})
