package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	kratosErrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"

	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/user"

	"go-wind-admin/pkg/wechat/wechattest"
)

func newMiniappAuthServiceForTest(e *miniappEnv, logger *bLogger.Helper, runtime *MiniappRuntime) *MiniappAuthService {
	return &MiniappAuthService{
		log:                logger,
		miniapp:            runtime,
		authn:              e.authn,
		userRepo:           e.userRepo,
		userCredentialRepo: e.credRepo,
		roleRepo:           e.authn.roleRepo,
		revoker:            e.revoker,
	}
}

// requireReason 断言错误的 HTTP 状态码和 reason。
func requireReason(t *testing.T, err error, code int, reason string, msgAndArgs ...any) {
	t.Helper()
	require.Error(t, err, msgAndArgs...)
	se := kratosErrors.FromError(err)
	require.Equal(t, reason, se.Reason, msgAndArgs...)
	require.Equal(t, int32(code), se.Code, msgAndArgs...)
}

// ---- 一键登录和绑定 ----

func TestMiniappPhoneLogin_Success(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")

	resp, err := e.phoneLogin("10.0.0.1", "openid-zhangsan-0001", "13800000001")
	require.NoError(t, err)
	require.Equal(t, authenticationV1.TokenType_bearer, resp.GetTokenType())
	require.True(t, resp.GetMiniappBound())
	require.Equal(t, int64(e.authenticator.GetAccessTokenExpires(authenticationV1.ClientType_app).Seconds()), resp.GetExpiresIn())

	// 令牌是 app 类型，带着算好的角色。
	payload := e.payload(resp.GetAccessToken())
	require.NotNil(t, payload)
	require.Equal(t, uid, payload.GetUserId())
	require.Equal(t, authenticationV1.ClientType_app, payload.GetClientType())
	require.Equal(t, []string{"miniapp-user"}, payload.GetRoles())

	// 绑定行：一行，字段按约定；credential 是空值而不是空串。
	rows := e.bindings(uid)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, "openid-zhangsan-0001", *row.ProviderAccountID)
	require.Equal(t, "openid-zhangsan-0001", *row.Identifier)
	require.Equal(t, "SOCIAL_OAUTH", string(*row.IdentityType))
	require.Equal(t, "OAUTH_TOKEN", string(*row.CredentialType))
	require.Equal(t, "ENABLED", string(*row.Status))
	require.Equal(t, uint32(0), *row.TenantID)
	require.Nil(t, row.Credential, "credential 字段不设置")
	require.Nil(t, row.ExtraInfo)
	var isNull bool
	require.NoError(t, e.entClient.DB().QueryRowContext(context.Background(),
		"SELECT credential IS NULL FROM sys_user_credentials WHERE id = ?", row.ID).Scan(&isNull))
	require.True(t, isNull, "库里是 NULL，不是空串")

	// 会话元数据记着所用的绑定；没有 refresh token。
	meta, err := e.authenticator.GetSessionMeta(context.Background(), authenticationV1.ClientType_app, uid, payload.GetJti())
	require.NoError(t, err)
	require.Equal(t, row.ID, meta.BindingID)
	require.Equal(t, "zhangsan", meta.Username)
	require.Equal(t, "10.0.0.1", meta.Ip)
	require.Empty(t, e.authenticator.GetRefreshTokensForTest(context.Background(), authenticationV1.ClientType_app, uid))
	// 元数据的 TTL 取 access 有效期。
	require.Equal(t, e.authenticator.GetAccessTokenExpires(authenticationV1.ClientType_app),
		e.mr.TTL(fmt.Sprintf("us:1:%d:%s", uid, payload.GetJti())))

	// 再登录一次：绑定已有则不动，还是那一行。
	resp, err = e.phoneLogin("10.0.0.1", "openid-zhangsan-0001", "13800000001")
	require.NoError(t, err)
	require.True(t, resp.GetMiniappBound())
	require.Len(t, e.bindings(uid), 1)
	require.Equal(t, row.ID, e.bindings(uid)[0].ID)

	// 一个账号可以绑多个微信。
	_, err = e.phoneLogin("10.0.0.1", "openid-zhangsan-0002", "13800000001")
	require.NoError(t, err)
	require.Len(t, e.bindings(uid), 2)

	e.requireLogsClean("13800000001", "openid-zhangsan-0001")
}

func TestMiniappPhoneLogin_AccountRejections(t *testing.T) {
	e := newMiniappEnv(t)
	e.seedUser("dup-a", "13800000002")
	e.seedUser("dup-b", "13800000002")
	disabled := e.seedUser("disabled", "13800000003")
	e.client.User.UpdateOneID(disabled).SetStatus(user.StatusDisabled).ExecX(e.sys)
	// 角色没有「访问后台」权限点。
	plainRole := e.seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "no-backend", nil, nil, nil)
	noAccess := e.seedUser("noaccess", "13800000004", plainRole)

	// 手机号没命中。
	_, err := e.phoneLogin("10.0.0.1", "openid-a", "13800009999")
	requireReason(t, err, 403, "MINIAPP_PHONE_NOT_REGISTERED")

	// 命中多行。
	_, err = e.phoneLogin("10.0.0.1", "openid-a", "13800000002")
	requireReason(t, err, 409, "MINIAPP_PHONE_AMBIGUOUS")

	// 用户停用：不写绑定。
	_, err = e.phoneLogin("10.0.0.1", "openid-b", "13800000003")
	requireReason(t, err, 403, "FORBIDDEN")
	require.Empty(t, e.bindings(disabled))
	require.Empty(t, e.appTokens(disabled))

	// 没有后台访问权限：不签发。
	_, err = e.phoneLogin("10.0.0.1", "openid-c", "13800000004")
	requireReason(t, err, 403, "FORBIDDEN")
	require.Empty(t, e.appTokens(noAccess))

	e.requireLogsClean("13800009999", "13800000002", "13800000003")
}

func TestMiniappPhoneLogin_WechatFailures(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	ctx := e.reqCtx("10.0.0.1")
	login := func(code, phoneCode string) error {
		_, err := e.mini.LoginByPhone(ctx, &authenticationV1.MiniappPhoneLoginRequest{Code: code, PhoneCode: phoneCode})
		return err
	}
	// 每个用例换一个微信：同一个 openid 10 分钟内只能换 5 次手机号。
	newCode := func() string { return e.wxCode(fmt.Sprintf("openid-fail-%d", e.seq.Add(1))) }

	// code 已使用。
	used := e.wxCode("openid-1")
	require.NoError(t, login(used, e.phoneCode("13800000001")))
	requireReason(t, login(used, e.phoneCode("13800000001")), 400, "MINIAPP_CODE_INVALID")

	// code 过期（微信不认识）。
	requireReason(t, login("expired-SECRETCODE", e.phoneCode("13800000001")), 400, "MINIAPP_CODE_INVALID")
	// 手机号 code 无效。
	requireReason(t, login(newCode(), "expired-phone-SECRETCODE"), 400, "MINIAPP_CODE_INVALID")

	// 水印的 AppID 不符。
	e.wx.AddPhone("p-other-app", wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: "13800000001", WatermarkAppID: "wx-someone-else"})
	requireReason(t, login(newCode(), "p-other-app"), 400, "MINIAPP_CODE_INVALID")

	// 微信响应缺字段。
	e.wx.AddPhone("p-empty", wechattest.PhoneReply{OmitPhoneInfo: true})
	requireReason(t, login(newCode(), "p-empty"), 400, "MINIAPP_CODE_INVALID")

	// 区号不是 86：按未开通处理，哪怕号码本身能匹配到用户。
	e.wx.AddPhone("p-hk", wechattest.PhoneReply{CountryCode: "852", PurePhoneNumber: "13800000001"})
	requireReason(t, login(newCode(), "p-hk"), 403, "MINIAPP_PHONE_NOT_REGISTERED")

	// 号码格式不符。
	e.wx.AddPhone("p-short", wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: "1380000"})
	requireReason(t, login(newCode(), "p-short"), 403, "MINIAPP_PHONE_NOT_REGISTERED")

	// 微信超时。
	tokensBefore := len(e.appTokens(uid))
	e.wx.SetHang(wechattest.PathCode2Session, true)
	requireReason(t, login(newCode(), e.phoneCode("13800000001")), 503, "MINIAPP_UNAVAILABLE")
	e.wx.SetHang(wechattest.PathCode2Session, false)
	e.wx.SetHang(wechattest.PathGetPhone, true)
	requireReason(t, login(newCode(), e.phoneCode("13800000001")), 503, "MINIAPP_UNAVAILABLE")
	e.wx.SetHang(wechattest.PathGetPhone, false)
	// 微信报错（不是 code 的问题）。
	e.wx.SetError(wechattest.PathCode2Session, 45011)
	requireReason(t, login(newCode(), e.phoneCode("13800000001")), 503, "MINIAPP_UNAVAILABLE")
	e.wx.SetError(wechattest.PathCode2Session, 0)

	require.Len(t, e.appTokens(uid), tokensBefore, "失败的请求不签发令牌")
	e.requireLogsClean("13800000001")
}

// openid 已绑定别的账号：拒绝，原绑定没变，不自动改挂。
func TestMiniappPhoneLogin_BoundToOther(t *testing.T) {
	e := newMiniappEnv(t)
	owner := e.seedUser("owner", "13800000001")
	other := e.seedUser("other", "13800000002")

	_, err := e.phoneLogin("10.0.0.1", "openid-shared", "13800000001")
	require.NoError(t, err)
	original := e.bindings(owner)[0]

	_, err = e.phoneLogin("10.0.0.1", "openid-shared", "13800000002")
	requireReason(t, err, 409, "MINIAPP_BOUND_TO_OTHER")
	require.Empty(t, e.bindings(other))
	require.Empty(t, e.appTokens(other))
	rows := e.bindings(owner)
	require.Len(t, rows, 1)
	require.Equal(t, original.ID, rows[0].ID)

	// 密码路径也一样：确认 openid 属于别的账号时拒绝登录。
	_, err = e.passwordLogin("10.0.0.1", "other", defaultPassword, "openid-shared")
	requireReason(t, err, 409, "MINIAPP_BOUND_TO_OTHER")
	require.Empty(t, e.appTokens(other))
	require.Equal(t, owner, *e.bindings(owner)[0].UserID)
}

// 并发首次绑定：同用户的两个请求都成功且只有一行；不同用户的一个成功一个被拒。
func TestMiniappPhoneLogin_ConcurrentFirstBinding(t *testing.T) {
	e := newMiniappEnv(t)
	a := e.seedUser("user-a", "13800000001")
	b := e.seedUser("user-b", "13800000002")
	// 共享缓存的内存 SQLite 多连接并发写会报 table is locked。限成一个连接，事务之间排队，
	// 相当于数据库把两个事务串行化；真正交错的情况由 PostgreSQL 上的并发测试覆盖
	// （data 包 TestBindMiniapp_ConcurrentOnPostgres）。
	e.entClient.DB().SetMaxOpenConns(1)

	run := func(openid string, mobiles ...string) []error {
		errs := make([]error, len(mobiles))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, mobile := range mobiles {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = e.phoneLogin(fmt.Sprintf("10.0.1.%d", i+1), openid, mobile)
			}()
		}
		close(start)
		wg.Wait()
		return errs
	}

	// 同一个用户、同一个微信，两个请求同时到。
	for _, err := range run("openid-same-user", "13800000001", "13800000001") {
		require.NoError(t, err)
	}
	require.Len(t, e.bindings(a), 1)

	// 两个用户同时用同一个微信首次绑定。
	errs := run("openid-contended", "13800000001", "13800000002")
	var okCount int
	for _, err := range errs {
		if err == nil {
			okCount++
			continue
		}
		requireReason(t, err, 409, "MINIAPP_BOUND_TO_OTHER")
	}
	require.Equal(t, 1, okCount, "errs=%v", errs)
	// 两个 openid 各一行：第一个在 a 上，第二个落在抢到的那一方。
	require.Equal(t, 2, len(e.bindings(a))+len(e.bindings(b)))
}

// ---- 账号密码登录（client_type=app）----

func TestMiniappPasswordLogin(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")

	// 带 wx.login 的 code：登录并建立绑定。
	resp, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-pw-0001")
	require.NoError(t, err)
	require.True(t, resp.GetMiniappBound())
	payload := e.payload(resp.GetAccessToken())
	require.Equal(t, authenticationV1.ClientType_app, payload.GetClientType())
	rows := e.bindings(uid)
	require.Len(t, rows, 1)
	meta, err := e.authenticator.GetSessionMeta(context.Background(), authenticationV1.ClientType_app, uid, payload.GetJti())
	require.NoError(t, err)
	require.Equal(t, rows[0].ID, meta.BindingID)
	// 只有 access token：不写 refresh token（也就没有 Cookie 可发）。
	require.Empty(t, e.authenticator.GetRefreshTokensForTest(context.Background(), authenticationV1.ClientType_app, uid))

	// H5 不带 code：登录成功，miniapp_bound=false，会话不记绑定。
	resp, err = e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "")
	require.NoError(t, err)
	require.NotNil(t, resp.MiniappBound, "app 登录的响应总是带 miniapp_bound")
	require.False(t, resp.GetMiniappBound())
	payload = e.payload(resp.GetAccessToken())
	meta, err = e.authenticator.GetSessionMeta(context.Background(), authenticationV1.ClientType_app, uid, payload.GetJti())
	require.NoError(t, err)
	require.Zero(t, meta.BindingID)
	require.Len(t, e.bindings(uid), 1)

	// 密码错：不签发，不调微信。
	calls := e.wx.Calls()
	_, err = e.passwordLogin("10.0.0.1", "zhangsan", "Wrong@Pass1", "openid-pw-0002")
	requireReason(t, err, 400, "INVALID_PASSWORD")
	require.Equal(t, calls, e.wx.Calls())

	// 管理端登录不受影响：响应里没有 miniapp_bound，令牌是 admin 类型。
	resp, err = e.adminPasswordLogin("10.0.0.1", "zhangsan", defaultPassword)
	require.NoError(t, err)
	require.Nil(t, resp.MiniappBound)
	require.Equal(t, authenticationV1.ClientType_admin, e.payload(resp.GetAccessToken()).GetClientType())

	e.requireLogsClean("openid-pw-0001")
}

// app_id 为空（未启用）时三条路径都拒绝。
func TestMiniappLogin_DisabledWhenAppIDEmpty(t *testing.T) {
	e := newMiniappEnv(t)
	e.seedUser("zhangsan", "13800000001")
	e.authn.miniapp = nil
	e.mini.miniapp = nil

	_, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "")
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.mini.LoginByPhone(e.reqCtx("10.0.0.1"), &authenticationV1.MiniappPhoneLoginRequest{Code: "c", PhoneCode: "p"})
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	_, err = e.mini.SilentLogin(e.reqCtx("10.0.0.1"), &authenticationV1.MiniappSilentLoginRequest{Code: "c"})
	requireReason(t, err, 503, "MINIAPP_UNAVAILABLE")
	require.Zero(t, e.wx.Calls())
}

// ---- 静默续期 ----

func TestMiniappSilentLogin(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	_, err := e.phoneLogin("10.0.0.1", "openid-zs-0001", "13800000001")
	require.NoError(t, err)
	bindingID := e.bindings(uid)[0].ID

	// 成功：新令牌，仍记着同一条绑定；不调换号接口。
	phoneCalls := e.wx.Calls().GetPhone
	resp, err := e.silentLogin("10.0.0.1", "openid-zs-0001")
	require.NoError(t, err)
	require.True(t, resp.GetMiniappBound())
	payload := e.payload(resp.GetAccessToken())
	require.Equal(t, uid, payload.GetUserId())
	require.Equal(t, []string{"miniapp-user"}, payload.GetRoles())
	meta, err := e.authenticator.GetSessionMeta(context.Background(), authenticationV1.ClientType_app, uid, payload.GetJti())
	require.NoError(t, err)
	require.Equal(t, bindingID, meta.BindingID)
	require.Equal(t, phoneCalls, e.wx.Calls().GetPhone)
	require.Len(t, e.bindings(uid), 1, "续期不写绑定")

	// 权限每次重新计算：多挂一个角色后续期，新令牌里有它。
	extra := e.seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "extra-role", []uint32{e.permID}, nil, nil)
	e.client.UserRole.Create().SetUserID(uid).SetRoleID(extra).SaveX(e.sys)
	resp, err = e.silentLogin("10.0.0.1", "openid-zs-0001")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"miniapp-user", "extra-role"}, e.payload(resp.GetAccessToken()).GetRoles())

	// 没有绑定的微信。
	_, err = e.silentLogin("10.0.0.1", "openid-stranger")
	requireReason(t, err, 403, "MINIAPP_NOT_BOUND")

	// 同一个用户换一个没绑定的微信来续期：被拒（续期认的是微信，不是用户）。
	_, err = e.silentLogin("10.0.0.1", "openid-zs-another-phone")
	requireReason(t, err, 403, "MINIAPP_NOT_BOUND")

	// code 无效。
	_, err = e.mini.SilentLogin(e.reqCtx("10.0.0.1"), &authenticationV1.MiniappSilentLoginRequest{Code: "expired-SECRETCODE"})
	requireReason(t, err, 400, "MINIAPP_CODE_INVALID")

	// 用户停用：拒绝，绑定不删（停用不删绑定，恢复后还能续期）。
	e.client.User.UpdateOneID(uid).SetStatus(user.StatusDisabled).ExecX(e.sys)
	_, err = e.silentLogin("10.0.0.1", "openid-zs-0001")
	requireReason(t, err, 403, "FORBIDDEN")
	require.Len(t, e.bindings(uid), 1)

	e.requireLogsClean("openid-zs-0001")
}

// ---- 生效点 ----

// 「先读、撤销完成、再签发」：app 密码登录读完撤销版本并通过认证后，改密完成，再签发，
// 返回 CONFLICT，没有令牌。最终状态：库里没有这个用户的绑定，用旧密码登录失败。
func TestMiniappPasswordLogin_PasswordChangedBeforeIssue(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	const newPassword = "Brand#New9Pass"

	// 登录已经读了版本、校验了旧密码，正要调微信的那一刻：管理员重置密码并完成吊销。
	e.hook.beforeCode2Session = func() {
		adminCtx := opCtx(e.sys, 9001, 0, true)
		_, err := e.users.EditUserPassword(adminCtx, &identityV1.EditUserPasswordRequest{
			UserId: uid, NewPassword: encryptLoginPassword(t, newPassword),
		})
		require.NoError(t, err)
	}

	_, err := e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "openid-race-0001")
	requireReason(t, err, 409, "CONFLICT")
	require.Empty(t, e.appTokens(uid), "没有令牌留下")
	require.Empty(t, e.bindings(uid), "绑定事务重新确认时发现密码已变，不写绑定")

	// 旧密码不能再登录，新密码可以。
	_, err = e.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "")
	requireReason(t, err, 400, "INVALID_PASSWORD")
	resp, err := e.passwordLogin("10.0.0.1", "zhangsan", newPassword, "openid-race-0001")
	require.NoError(t, err)
	require.True(t, resp.GetMiniappBound())
}

// 一键登录和静默续期也按撤销版本条件写入：读完版本之后 app 令牌被全量吊销，签发返回 CONFLICT。
func TestMiniappLogin_RevokedBeforeIssue(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	_, err := e.phoneLogin("10.0.0.1", "openid-zs-0001", "13800000001")
	require.NoError(t, err)

	revoke := func() {
		require.NoError(t, e.authenticator.RevokeUserToken(context.Background(), authenticationV1.ClientType_app, uid))
	}

	// 一键登录的版本在两次微信调用之后才读，用数据库钩子之外的办法插不进去；
	// 这里直接验证签发函数：拿旧版本去写，写不进去。
	version, err := e.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, uid)
	require.NoError(t, err)
	revoke()
	_, err = e.authn.issueAppSession(e.reqCtx("10.0.0.1"), &authenticationV1.UserTokenPayload{UserId: uid}, 0, version, nil)
	requireReason(t, err, 409, "CONFLICT")
	require.Empty(t, e.appTokens(uid))

	// 版本没变时写得进去。
	version, err = e.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, uid)
	require.NoError(t, err)
	token, err := e.authn.issueAppSession(e.reqCtx("10.0.0.1"), &authenticationV1.UserTokenPayload{UserId: uid}, 0, version, nil)
	require.NoError(t, err)
	require.True(t, e.tokenValid(token))
}

// 绑定事务的重新确认：认证之后密码或手机号变了，不写绑定。
func TestBindMiniapp_ReconfirmsCredentialUnderUserLock(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	ctx := context.Background()

	// 手机号路径：号码已经不是认证时的那个。
	stale := "13800000009"
	_, err := e.credRepo.BindMiniapp(ctx, uid, "openid-x", data.MiniappBindCheck{Mobile: &stale})
	require.ErrorIs(t, err, data.ErrMiniappBindStale)

	// 密码路径：快照不是当前的凭证值。
	old := "$2a$10$stale-hash"
	_, err = e.credRepo.BindMiniapp(ctx, uid, "openid-x", data.MiniappBindCheck{PasswordSnapshot: &old})
	require.ErrorIs(t, err, data.ErrMiniappBindStale)

	// 用户不是启用状态。
	e.client.User.UpdateOneID(uid).SetStatus(user.StatusLocked).ExecX(e.sys)
	current := "13800000001"
	_, err = e.credRepo.BindMiniapp(ctx, uid, "openid-x", data.MiniappBindCheck{Mobile: &current})
	require.ErrorIs(t, err, data.ErrMiniappUserDisabled)

	require.Empty(t, e.bindings(uid))

	// 条件都对：写入；再来一次返回同一行。
	e.client.User.UpdateOneID(uid).SetStatus(user.StatusNormal).ExecX(e.sys)
	id1, err := e.credRepo.BindMiniapp(ctx, uid, "openid-x", data.MiniappBindCheck{Mobile: &current})
	require.NoError(t, err)
	id2, err := e.credRepo.BindMiniapp(ctx, uid, "openid-x", data.MiniappBindCheck{Mobile: &current})
	require.NoError(t, err)
	require.Equal(t, id1, id2)
}
