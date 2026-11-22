// Package wechat 微信小程序服务端接口的封装。
//
// 只做三件事：wx.login 的 code 换 openid，getPhoneNumber 的 code 换手机号，
// 以及换手机号要用的 access_token。对外只返回 *Error，里面没有 URL、请求、
// 响应正文和微信的 errmsg：URL 里带着 AppSecret 或 access_token，不能进日志。
package wechat

import (
	"context"
	"errors"
	"fmt"
)

// MiniappClient 小程序登录要用的两个兑换接口。
type MiniappClient interface {
	// Code2Session 用 wx.login 的 code 换 openid。
	Code2Session(ctx context.Context, code string) (openid string, err error)
	// GetPhoneNumber 用 getPhoneNumber 的 code 换手机号。实现负责校验 AppID 水印。
	GetPhoneNumber(ctx context.Context, phoneCode string) (*Phone, error)
}

// Phone 微信返回的手机号。
type Phone struct {
	CountryCode     string // 区号，中国大陆是 "86"
	PurePhoneNumber string // 不带区号的号码
}

// Kind 错误的两种去向：code 本身不行（让用户重新授权），或者微信这一侧暂时不可用（可重试）。
type Kind int

const (
	// KindUnavailable 微信超时、报错、配置不对、access_token 取不到。
	KindUnavailable Kind = iota
	// KindCodeInvalid code 无效、已使用、过期；手机号水印的 AppID 不符；响应缺字段。
	KindCodeInvalid
)

func (k Kind) String() string {
	if k == KindCodeInvalid {
		return "code_invalid"
	}
	return "unavailable"
}

// 接口名，写在 Error.API 里。
const (
	APICode2Session   = "code2session"
	APIGetPhoneNumber = "getuserphonenumber"
	APIStableToken    = "stable_token"
)

// Error 是适配器对外返回的唯一错误类型，不包含 URL、请求、响应正文和微信的 errmsg。
type Error struct {
	API     string // APICode2Session / APIGetPhoneNumber / APIStableToken
	ErrCode int    // 微信的数值错误码，网络错误为 0
	Kind    Kind
}

func (e *Error) Error() string {
	return fmt.Sprintf("wechat %s failed: kind=%s errcode=%d", e.API, e.Kind, e.ErrCode)
}

// IsCodeInvalid err 是否表示 code 本身无效（区别于微信暂时不可用）。
func IsCodeInvalid(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindCodeInvalid
}
