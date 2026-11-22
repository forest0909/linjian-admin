package wechat_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"go-wind-admin/pkg/wechat"
	"go-wind-admin/pkg/wechat/wechattest"
)

func newClient(t *testing.T, srv *wechattest.Server) (wechat.MiniappClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return wechat.NewMiniappClient(wechat.Config{
		AppID:     wechattest.AppID,
		AppSecret: wechattest.AppSecret,
		BaseURL:   srv.URL,
		// 超时用例不等 5 秒。
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
	}, rdb), mr
}

// requireSafe 错误必须是 *wechat.Error，文本里不能有 Secret、access_token、code 和 URL。
func requireSafe(t *testing.T, err error, secrets ...string) *wechat.Error {
	t.Helper()
	require.Error(t, err)
	var we *wechat.Error
	require.True(t, errors.As(err, &we), "适配器只能返回 *wechat.Error，got %T", err)
	text := err.Error()
	for _, secret := range append(secrets, wechattest.AppSecret, wechattest.AccessToken, "http://", "https://", "127.0.0.1") {
		require.NotContains(t, text, secret)
	}
	return we
}

func TestCode2Session(t *testing.T) {
	srv := wechattest.New(t)
	c, _ := newClient(t, srv)
	ctx := context.Background()

	srv.AddCode("code-ok", "openid-1")
	openid, err := c.Code2Session(ctx, "code-ok")
	require.NoError(t, err)
	require.Equal(t, "openid-1", openid)

	// 同一个 code 再换一次：已使用。
	_, err = c.Code2Session(ctx, "code-ok")
	we := requireSafe(t, err, "code-ok")
	require.Equal(t, wechat.KindCodeInvalid, we.Kind)
	require.Equal(t, 40163, we.ErrCode)

	// 没登记的 code：无效或过期。
	_, err = c.Code2Session(ctx, "code-expired")
	we = requireSafe(t, err, "code-expired")
	require.Equal(t, wechat.KindCodeInvalid, we.Kind)
	require.True(t, wechat.IsCodeInvalid(err))

	// 微信报别的错（频率限制）：不可用，不是 code 的问题。
	srv.SetError(wechattest.PathCode2Session, 45011)
	_, err = c.Code2Session(ctx, "code-any")
	we = requireSafe(t, err, "code-any")
	require.Equal(t, wechat.KindUnavailable, we.Kind)
	require.Equal(t, 45011, we.ErrCode)
	require.False(t, wechat.IsCodeInvalid(err))
}

// 真实实现对 mock_ 开头的 code 没有特殊处理：照常请求微信，按微信的应答（无效）处理。
func TestRealClient_MockPrefixedCodeIsNotSpecial(t *testing.T) {
	srv := wechattest.New(t)
	c, _ := newClient(t, srv)
	ctx := context.Background()

	_, err := c.Code2Session(ctx, "mock_openid_dev")
	require.True(t, wechat.IsCodeInvalid(err), "got %v", err)
	require.Equal(t, 1, srv.Calls().Code2Session, "应该真的去请求了微信")

	_, err = c.GetPhoneNumber(ctx, "mock_phone_13800000000")
	require.True(t, wechat.IsCodeInvalid(err), "got %v", err)
	require.Equal(t, 1, srv.Calls().GetPhone)
}

func TestGetPhoneNumber(t *testing.T) {
	srv := wechattest.New(t)
	c, mr := newClient(t, srv)
	ctx := context.Background()

	srv.AddPhone("p-ok", wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: "13800000001"})
	phone, err := c.GetPhoneNumber(ctx, "p-ok")
	require.NoError(t, err)
	require.Equal(t, &wechat.Phone{CountryCode: "86", PurePhoneNumber: "13800000001"}, phone)

	// access_token 缓存在 Redis，TTL 是 expires_in 减 300 秒；第二次不再取。
	key := "gowind:wechat:miniapp:access_token:" + wechattest.AppID
	require.Equal(t, 6900*time.Second, mr.TTL(key))
	_, err = c.GetPhoneNumber(ctx, "p-ok")
	require.NoError(t, err)
	require.Equal(t, 1, srv.Calls().StableToken)

	// 水印的 AppID 不是自己的。
	srv.AddPhone("p-other-app", wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: "13800000002", WatermarkAppID: "wx-someone-else"})
	_, err = c.GetPhoneNumber(ctx, "p-other-app")
	we := requireSafe(t, err, "p-other-app", "13800000002")
	require.Equal(t, wechat.KindCodeInvalid, we.Kind)

	// 应答缺 phone_info。
	srv.AddPhone("p-empty", wechattest.PhoneReply{OmitPhoneInfo: true})
	_, err = c.GetPhoneNumber(ctx, "p-empty")
	require.True(t, wechat.IsCodeInvalid(err), "got %v", err)

	// code 无效。
	_, err = c.GetPhoneNumber(ctx, "p-unknown")
	we = requireSafe(t, err, "p-unknown")
	require.Equal(t, wechat.KindCodeInvalid, we.Kind)
	require.Equal(t, 40029, we.ErrCode)
}

// 微信明确说 access_token 无效：删缓存、重取、重试一次。
func TestGetPhoneNumber_RefetchesTokenOnceWhenInvalid(t *testing.T) {
	srv := wechattest.New(t)
	c, _ := newClient(t, srv)
	ctx := context.Background()
	srv.AddPhone("p-ok", wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: "13800000001"})

	_, err := c.GetPhoneNumber(ctx, "p-ok")
	require.NoError(t, err)

	srv.InvalidateTokenOnce()
	phone, err := c.GetPhoneNumber(ctx, "p-ok")
	require.NoError(t, err)
	require.Equal(t, "13800000001", phone.PurePhoneNumber)
	calls := srv.Calls()
	require.Equal(t, 2, calls.StableToken, "token 失效后重取一次")
	require.Equal(t, 3, calls.GetPhone, "第二次调用里失败一次、重试一次")

	// 一直无效：只重试一次，然后报不可用。
	srv.SetError(wechattest.PathGetPhone, 40001)
	_, err = c.GetPhoneNumber(ctx, "p-ok")
	we := requireSafe(t, err, "p-ok")
	require.Equal(t, wechat.KindUnavailable, we.Kind)
	require.Equal(t, 5, srv.Calls().GetPhone)
}

// 微信超时：返回不可用，不重试；错误里没有 URL（URL 里带着 Secret 或 access_token）。
func TestTimeout_ErrorCarriesNoURL(t *testing.T) {
	srv := wechattest.New(t)
	c, _ := newClient(t, srv)
	ctx := context.Background()

	srv.SetHang(wechattest.PathCode2Session, true)
	_, err := c.Code2Session(ctx, "code-timeout")
	we := requireSafe(t, err, "code-timeout")
	require.Equal(t, wechat.KindUnavailable, we.Kind)
	require.Zero(t, we.ErrCode)
	require.Equal(t, 1, srv.Calls().Code2Session)

	srv.AddPhone("p-ok", wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: "13800000001"})
	srv.SetHang(wechattest.PathGetPhone, true)
	_, err = c.GetPhoneNumber(ctx, "p-ok")
	we = requireSafe(t, err, "p-ok")
	require.Equal(t, wechat.APIGetPhoneNumber, we.API)
	require.Equal(t, 1, srv.Calls().GetPhone, "网络超时不重试")

	srv.SetHang(wechattest.PathGetPhone, false)
	srv.SetHang(wechattest.PathStableToken, true)
	c2, _ := newClient(t, srv) // 新的 Redis，没有缓存的 token
	_, err = c2.GetPhoneNumber(ctx, "p-ok")
	we = requireSafe(t, err, "p-ok")
	require.Equal(t, wechat.APIStableToken, we.API)
}

// 配置不对的三种情况：Secret 错、地址连不上、地址不是合法 URL。错误里都没有 Secret 和 URL。
func TestBadConfig_ErrorCarriesNoSecret(t *testing.T) {
	srv := wechattest.New(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	const wrongSecret = "WRONG-secret-value"

	// Secret 错：微信返回错误码，errmsg 回显了请求地址。
	c := wechat.NewMiniappClient(wechat.Config{AppID: wechattest.AppID, AppSecret: wrongSecret, BaseURL: srv.URL}, rdb)
	_, err := c.Code2Session(ctx, "code-x")
	we := requireSafe(t, err, wrongSecret, "code-x")
	require.Equal(t, wechat.KindUnavailable, we.Kind)
	require.Equal(t, 40125, we.ErrCode)
	_, err = c.GetPhoneNumber(ctx, "p-x")
	we = requireSafe(t, err, wrongSecret, "p-x")
	require.Equal(t, wechat.APIStableToken, we.API)

	// 地址连不上：net/http 的 *url.Error 带完整 URL，适配器要截断。
	c = wechat.NewMiniappClient(wechat.Config{
		AppID: wechattest.AppID, AppSecret: wrongSecret, BaseURL: "http://127.0.0.1:1",
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
	}, rdb)
	_, err = c.Code2Session(ctx, "code-y")
	requireSafe(t, err, wrongSecret, "code-y")
	_, err = c.GetPhoneNumber(ctx, "p-y")
	requireSafe(t, err, wrongSecret, "p-y")

	// 地址不是合法 URL。
	c = wechat.NewMiniappClient(wechat.Config{AppID: wechattest.AppID, AppSecret: wrongSecret, BaseURL: "http://bad host\x7f"}, rdb)
	_, err = c.Code2Session(ctx, "code-z")
	requireSafe(t, err, wrongSecret, "code-z")
	_, err = c.GetPhoneNumber(ctx, "p-z")
	requireSafe(t, err, wrongSecret, "p-z")
}

// Redis 读不出 access_token：不可用，不去调微信。
func TestGetPhoneNumber_RedisDown(t *testing.T) {
	srv := wechattest.New(t)
	c, mr := newClient(t, srv)
	mr.Close()

	_, err := c.GetPhoneNumber(context.Background(), "p-ok")
	we := requireSafe(t, err, "p-ok")
	require.Equal(t, wechat.KindUnavailable, we.Kind)
	require.Equal(t, wechat.APIStableToken, we.API)
	require.Zero(t, srv.Calls().StableToken)
}
