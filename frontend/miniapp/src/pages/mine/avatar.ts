import Taro from '@tarojs/taro'

/** 和后端的头像上限一致（5 MiB）。 */
export const MAX_AVATAR_BYTES = 5 * 1024 * 1024

export interface PickedImage {
  /** 不带 `data:` 前缀的纯 base64。 */
  base64: string
  size: number
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const dataUrl = String(reader.result)
      resolve(dataUrl.slice(dataUrl.indexOf(',') + 1))
    }
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(blob)
  })
}

function readFileBase64(path: string): Promise<string> {
  return new Promise((resolve, reject) => {
    Taro.getFileSystemManager().readFile({
      filePath: path,
      encoding: 'base64',
      success: (res) => resolve(String(res.data)),
      fail: reject,
    })
  })
}

/** 让用户选一张图，读成 base64。用户取消时返回 null。 */
export async function pickImage(): Promise<PickedImage | null> {
  let chosen: Taro.chooseImage.SuccessCallbackResult
  try {
    chosen = await Taro.chooseImage({ count: 1, sizeType: ['compressed'], sourceType: ['album', 'camera'] })
  } catch (err) {
    const msg = (err as { errMsg?: string } | null)?.errMsg ?? ''
    if (msg.includes('cancel')) {
      return null
    }
    throw err
  }
  const file = chosen.tempFiles[0]
  if (!file) {
    return null
  }
  if (process.env.TARO_ENV === 'h5') {
    // H5 下 tempFilePath 是 blob URL，没有文件系统接口
    const blob = file.originalFileObj ?? (await (await fetch(file.path)).blob())
    return { base64: await blobToBase64(blob), size: blob.size }
  }
  return { base64: await readFileBase64(file.path), size: file.size }
}
