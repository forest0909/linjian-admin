package service

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	"go-wind-admin/app/admin/service/internal/data"

	"go-wind-admin/pkg/wechat/wechattest"
)

// 失败合同：设计方案 5.6 的表，三条登录路径在各种失败下的响应，逐格断言。
// 「不调微信」用假服务端各接口的调用次数验证。

// contractEnv 一个已经有绑定的用户（静默续期要用），外加一个还没绑定的微信。
type contractEnv struct {
	*miniappEnv
	uid uint32
}

const (
	contractMobile      = "13800000001"
	contractBoundOpenID = "openid-contract-bound"
)

func newContractEnv(t *testing.T) *contractEnv {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", contractMobile)
	_, err := e.phoneLogin("10.9.9.9", contractBoundOpenID, contractMobile)
	require.NoError(t, err)
	return &contractEnv{miniappEnv: e, uid: uid}
}

// 第一阶段超额（IP 或 AppID 总量）：一键登录和续期 429；密码路径登录继续、miniapp_bound=false。
// 三条路径都不调微信：假服务端三个接口的计数都没动。
func TestFailureContract_RequestQuotaExceeded(t *testing.T) {
	for name, key := range map[string]string{
		"IP 超额":      miniappQuotaKeys + "ip:10.0.0.1",
		"AppID 总量超额": miniappQuotaKeys + "app:" + wechattest.AppID,
	} {
		t.Run(name, func(t *testing.T) {
			e := newContractEnv(t)
			limit := data.MiniappQuotaIPLimit
			if name != "IP 超额" {
				limit = data.MiniappQuotaAppLimit
			}
			require.NoError(t, e.mr.Set(key, strconv.Itoa(limit)))
			before := e.wx.Calls()
			tokens := len(e.appTokens(e.uid))

			_, err := e.phoneLogin("10.0.0.1", "openid-new", contractMobile)
			requireReason(t, err, 429, "TOO_MANY_REQUESTS")
			_, err = e.silentLogin("10.0.0.1", contractBoundOpenID)
			requireReason(t, err, 429, "TOO_MANY_REQUESTS")
			require.Len(t, e.appTokens(e.uid), tokens)

			resp, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-new")
			require.NoError(t, err)
			require.False(t, resp.GetMiniappBound())
			require.True(t, e.tokenValid(resp.GetAccessToken()))

			require.Equal(t, before, e.wx.Calls(), "超额的请求不调微信")
			require.Len(t, e.bindings(e.uid), 1, "没有新绑定")
		})
	}
}

// openid 换号超额：429。已经调过 jscode2session；不取 access_token，不调换号接口。
func TestFailureContract_PhoneExchangeQuotaExceeded(t *testing.T) {
	e := newContractEnv(t)
	const openid = "openid-greedy"
	require.NoError(t, e.mr.Set(miniappQuotaKeys+"phone:"+openid, strconv.Itoa(data.MiniappQuotaPhoneLimit)))
	// 清掉 access_token 缓存：超额时连 stable_token 都不该去调。
	e.mr.Del("gowind:wechat:miniapp:access_token:" + wechattest.AppID)
	before := e.wx.Calls()

	_, err := e.phoneLogin("10.0.0.1", openid, contractMobile)
	requireReason(t, err, 429, "TOO_MANY_REQUESTS")

	after := e.wx.Calls()
	require.Equal(t, before.Code2Session+1, after.Code2Session)
	require.Equal(t, before.StableToken, after.StableToken)
	require.Equal(t, before.GetPhone, after.GetPhone)
}

// 额度用的 Redis 故障：一键登录和续期 503，不调微信；密码路径在会话 Redis 可用时登录成功、miniapp_bound=false。
// 故障只注入到额度的键上：验证码和会话存储保持可用（修订 3 第 4 条）。
func TestFailureContract_QuotaRedisDown(t *testing.T) {
	e := newContractEnv(t)
	e.fault.failKeys(miniappQuotaKeys)
	before := e.wx.Calls()

	_, err := e.phoneLogin("10.0.0.1", "openid-new", contractMobile)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.silentLogin("10.0.0.1", contractBoundOpenID)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")

	resp, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-new")
	require.NoError(t, err)
	require.False(t, resp.GetMiniappBound())
	require.True(t, e.tokenValid(resp.GetAccessToken()))

	require.Equal(t, before, e.wx.Calls(), "额度扣不了就不调微信")
	require.Len(t, e.bindings(e.uid), 1)

	// 管理端密码登录不碰微信额度，照常。
	adminResp, err := e.adminPasswordLogin("10.0.0.1", "zhangsan", defaultPassword)
	require.NoError(t, err)
	require.True(t, e.tokenValid(adminResp.GetAccessToken()))
}

// 微信超时或报错：一键登录和续期 503；密码路径登录继续、miniapp_bound=false。
func TestFailureContract_WechatUnavailable(t *testing.T) {
	for name, breakWechat := range map[string]func(e *contractEnv){
		"超时": func(e *contractEnv) { e.wx.SetHang(wechattest.PathCode2Session, true) },
		"报错": func(e *contractEnv) { e.wx.SetError(wechattest.PathCode2Session, -1) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newContractEnv(t)
			breakWechat(e)
			tokens := len(e.appTokens(e.uid))

			_, err := e.phoneLogin("10.0.0.1", "openid-new", contractMobile)
			requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
			_, err = e.silentLogin("10.0.0.1", contractBoundOpenID)
			requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
			require.Len(t, e.appTokens(e.uid), tokens)

			resp, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-new")
			require.NoError(t, err)
			require.False(t, resp.GetMiniappBound())
			require.Len(t, e.bindings(e.uid), 1)
			e.requireLogsClean(contractMobile)
		})
	}
}

// 微信 code 无效：一键登录和续期 400；密码路径登录继续、miniapp_bound=false。
func TestFailureContract_WechatCodeInvalid(t *testing.T) {
	e := newContractEnv(t)
	ctx := e.reqCtx("10.0.0.1")

	_, err := e.mini.LoginByPhone(ctx, &authenticationV1.MiniappPhoneLoginRequest{Code: "bad-SECRETCODE", PhoneCode: e.phoneCode(contractMobile)})
	requireReason(t, err, 400, "MINIAPP_CODE_INVALID")
	_, err = e.mini.SilentLogin(ctx, &authenticationV1.MiniappSilentLoginRequest{Code: "bad-SECRETCODE"})
	requireReason(t, err, 400, "MINIAPP_CODE_INVALID")

	req := loginReq("zhangsan", encryptLoginPassword(t, defaultPassword))
	req.ClientType = authenticationV1.ClientType_app.Enum()
	bad := "bad-SECRETCODE"
	req.Code = &bad
	resp, err := e.authn.Login(e.captchaCtx("10.0.0.1"), req)
	require.NoError(t, err)
	require.False(t, resp.GetMiniappBound())
	e.requireLogsClean()
}

// 绑定事务失败：一键登录 503 且没有签发令牌；密码路径登录成功、miniapp_bound=false。
func TestFailureContract_BindTransactionFails(t *testing.T) {
	e := newContractEnv(t)
	e.failBindingInsert.Store(true)
	tokens := len(e.appTokens(e.uid))

	_, err := e.phoneLogin("10.0.0.1", "openid-new", contractMobile)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	require.Len(t, e.appTokens(e.uid), tokens, "绑定写不进去就不签发")

	resp, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-new")
	require.NoError(t, err)
	require.False(t, resp.GetMiniappBound())
	require.True(t, e.tokenValid(resp.GetAccessToken()))
	require.Len(t, e.bindings(e.uid), 1, "只有原来那条绑定")

	// 故障恢复后重试成功。
	e.failBindingInsert.Store(false)
	resp, err = e.phoneLogin("10.0.0.1", "openid-new", contractMobile)
	require.NoError(t, err)
	require.True(t, resp.GetMiniappBound())
}

// 会话 Redis 写失败：三条路径 503，已写的绑定仍在（没有补偿删除），重试即可。
func TestFailureContract_SessionWriteFails(t *testing.T) {
	e := newContractEnv(t)
	tokens := len(e.appTokens(e.uid))
	// 只让写 app 令牌的那一步失败：读撤销版本、额度、验证码都正常。
	e.fault.failKeys(fmt.Sprintf("at:1:%d:", e.uid))

	_, err := e.phoneLogin("10.0.0.1", "openid-phone-path", contractMobile)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-password-path")
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.silentLogin("10.0.0.1", contractBoundOpenID)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")

	e.fault.failKeys("")
	require.Len(t, e.appTokens(e.uid), tokens, "没有令牌写进去")
	openids := map[string]bool{}
	for _, row := range e.bindings(e.uid) {
		openids[*row.ProviderAccountID] = true
	}
	require.Equal(t, map[string]bool{contractBoundOpenID: true, "openid-phone-path": true, "openid-password-path": true}, openids,
		"认证成立后写的绑定保留")

	// 重试：凭已有的绑定直接续期成功。
	resp, err := e.silentLogin("10.0.0.1", "openid-phone-path")
	require.NoError(t, err)
	require.True(t, e.tokenValid(resp.GetAccessToken()))
}

// 撤销版本读不出来（会话 Redis 故障）也是 503，不签发。
func TestFailureContract_RevokeVersionReadFails(t *testing.T) {
	e := newContractEnv(t)
	e.fault.failKeys(fmt.Sprintf("rv:1:%d", e.uid))

	_, err := e.phoneLogin("10.0.0.1", "openid-new", contractMobile)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.silentLogin("10.0.0.1", contractBoundOpenID)
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "")
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
}

// 全是成功请求的洪峰也会被额度拦住：额度按请求计，不按失败计。
func TestFailureContract_SuccessfulFloodIsLimited(t *testing.T) {
	e := newContractEnv(t)
	for i := 0; i < data.MiniappQuotaIPLimit; i++ {
		_, err := e.silentLogin("10.0.0.5", contractBoundOpenID)
		require.NoError(t, err, "第 %d 次", i+1)
	}
	calls := e.wx.Calls().Code2Session
	_, err := e.silentLogin("10.0.0.5", contractBoundOpenID)
	requireReason(t, err, 429, "TOO_MANY_REQUESTS")
	require.Equal(t, calls, e.wx.Calls().Code2Session)

	// 别的 IP 不受影响。
	_, err = e.silentLogin("10.0.0.6", contractBoundOpenID)
	require.NoError(t, err)
}
