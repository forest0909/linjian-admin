//go:build miniapp_mock

package wechat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMockClient(t *testing.T) {
	c := NewMockMiniappClient()
	ctx := context.Background()

	openid, err := c.Code2Session(ctx, "mock_openid_dev")
	require.NoError(t, err)
	require.Equal(t, "dev", openid)

	phone, err := c.GetPhoneNumber(ctx, "mock_phone_13800000000")
	require.NoError(t, err)
	require.Equal(t, &Phone{CountryCode: "86", PurePhoneNumber: "13800000000"}, phone)

	// 其他输入一律按 code 无效处理，前缀后面是空的也一样。
	for _, code := range []string{"", "real-code", "mock_openid_", "mock_phone_13800000000"} {
		_, err = c.Code2Session(ctx, code)
		require.True(t, IsCodeInvalid(err), "Code2Session(%q): %v", code, err)
	}
	for _, code := range []string{"", "real-code", "mock_phone_", "mock_openid_dev"} {
		_, err = c.GetPhoneNumber(ctx, code)
		require.True(t, IsCodeInvalid(err), "GetPhoneNumber(%q): %v", code, err)
	}
}
