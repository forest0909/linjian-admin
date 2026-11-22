//go:build miniapp_mock

package wechat

import (
	"context"
	"strings"
)

// mock 输入的前缀。约定：
//
//	mock_openid_<串>   → openid 就是 <串>
//	mock_phone_<号码>  → 区号 86 和该号码
//
// 其他输入一律按 code 无效处理。
const (
	mockOpenIDPrefix = "mock_openid_"
	mockPhonePrefix  = "mock_phone_"
)

// mockClient 不调微信的开发用实现。只在带 miniapp_mock 标签的构建里存在，正式二进制里没有这段代码。
type mockClient struct{}

// NewMockMiniappClient 创建 mock 客户端。只能用于本机开发。
func NewMockMiniappClient() MiniappClient {
	return mockClient{}
}

func (mockClient) Code2Session(_ context.Context, code string) (string, error) {
	openid := strings.TrimPrefix(code, mockOpenIDPrefix)
	if openid == code || openid == "" {
		return "", &Error{API: APICode2Session, ErrCode: errCodeInvalidCode, Kind: KindCodeInvalid}
	}
	return openid, nil
}

func (mockClient) GetPhoneNumber(_ context.Context, phoneCode string) (*Phone, error) {
	number := strings.TrimPrefix(phoneCode, mockPhonePrefix)
	if number == phoneCode || number == "" {
		return nil, &Error{API: APIGetPhoneNumber, ErrCode: errCodeInvalidCode, Kind: KindCodeInvalid}
	}
	return &Phone{CountryCode: "86", PurePhoneNumber: number}, nil
}
