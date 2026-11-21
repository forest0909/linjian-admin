#!/usr/bin/env bash
# 检查微信 mock 进不了发布产物。CI 和本地都用：bash scripts/check-mock-guard.sh
#
# 1. 两端的构建在 TARO_APP_WECHAT_MOCK=true 下必须失败，而且是被 config/index.ts 的检查拦下的
#    （校验报错文案，别的原因导致的失败不算通过）。NODE_ENV 是 production 和 development 都要拦住。
# 2. 两端正常构建的产物里不能有 mock 的标记。
set -u
cd "$(dirname "$0")/.."

EXPECTED='TARO_APP_WECHAT_MOCK=true 不能用于生产构建'
MARKERS='mock_openid_dev|mock_phone_|（mock）'
fail=0

for target in mp-weixin h5; do
  for node_env in production development; do
    log=$(mktemp)
    if TARO_APP_WECHAT_MOCK=true NODE_ENV=$node_env npm run "build:$target" >"$log" 2>&1; then
      echo "FAIL build:$target NODE_ENV=$node_env：开着 mock 构建成功了" >&2
      fail=1
    elif ! grep -q "$EXPECTED" "$log"; then
      echo "FAIL build:$target NODE_ENV=$node_env：构建失败，但不是被 mock 检查拦下的" >&2
      tail -n 20 "$log" >&2
      fail=1
    else
      echo "ok   build:$target NODE_ENV=$node_env 被 mock 检查拦下"
    fi
    rm -f "$log"
  done
done

for target in mp-weixin h5; do
  dir="dist/$([ "$target" = h5 ] && echo h5 || echo weapp)"
  if ! npm run "build:$target" >/dev/null 2>&1; then
    echo "FAIL build:$target：正常构建失败" >&2
    fail=1
  elif grep -rlE "$MARKERS" "$dir" >&2; then
    echo "FAIL $dir：产物里有 mock 标记（见上面的文件）" >&2
    fail=1
  else
    echo "ok   $dir 里没有 mock 标记"
  fi
done

exit $fail
