import { beforeEach, describe, expect, it, vi } from 'vitest'

const taro = vi.hoisted(() => {
  const readFile = vi.fn()
  return { readFile, chooseImage: vi.fn(), getFileSystemManager: vi.fn(() => ({ readFile })) }
})
vi.mock('@tarojs/taro', () => ({ default: taro }))

import { MAX_AVATAR_BYTES, pickImage } from './avatar'

beforeEach(() => {
  taro.chooseImage.mockReset()
  taro.readFile.mockReset()
  taro.getFileSystemManager.mockClear()
})

describe('pickImage（小程序）', () => {
  it('超过上限：先按文件大小拒绝，不读文件、不转码', async () => {
    taro.chooseImage.mockResolvedValue({ tempFiles: [{ path: 'wxfile://big.jpg', size: MAX_AVATAR_BYTES + 1 }] })
    await expect(pickImage()).resolves.toEqual({ kind: 'too-large' })
    expect(taro.getFileSystemManager).not.toHaveBeenCalled()
    expect(taro.readFile).not.toHaveBeenCalled()
  })

  it('没超过上限：读成 base64', async () => {
    taro.chooseImage.mockResolvedValue({ tempFiles: [{ path: 'wxfile://ok.jpg', size: MAX_AVATAR_BYTES }] })
    taro.readFile.mockImplementation((opts: { filePath: string; encoding: string; success: (r: { data: string }) => void }) => {
      expect(opts).toMatchObject({ filePath: 'wxfile://ok.jpg', encoding: 'base64' })
      opts.success({ data: 'QUJD' })
    })
    await expect(pickImage()).resolves.toEqual({ kind: 'picked', base64: 'QUJD' })
  })

  it('用户取消：返回 cancelled；其他错误往上抛', async () => {
    taro.chooseImage.mockRejectedValueOnce({ errMsg: 'chooseImage:fail cancel' })
    await expect(pickImage()).resolves.toEqual({ kind: 'cancelled' })
    const denied = { errMsg: 'chooseImage:fail auth deny' }
    taro.chooseImage.mockRejectedValueOnce(denied)
    await expect(pickImage()).rejects.toBe(denied)
  })

  it('读文件失败：往上抛', async () => {
    taro.chooseImage.mockResolvedValue({ tempFiles: [{ path: 'wxfile://ok.jpg', size: 10 }] })
    const broken = { errMsg: 'readFile:fail' }
    taro.readFile.mockImplementation((opts: { fail: (e: unknown) => void }) => opts.fail(broken))
    await expect(pickImage()).rejects.toBe(broken)
  })
})
