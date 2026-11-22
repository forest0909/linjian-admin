package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	kratosErrors "github.com/go-kratos/kratos/v2/errors"
	ktransport "github.com/go-kratos/kratos/v2/transport"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/third_party/go-utils/trans"
	authnEngine "go-wind-admin/third_party/kratos-authn/engine"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/app/admin/service/internal/data"

	"go-wind-admin/pkg/constants"
	"go-wind-admin/pkg/middleware/auth"
)

// bearerTransporter 只带请求头的服务端 transport，给 auth 中间件取 Bearer 令牌用。
type bearerTransporter struct{ hdr http.Header }

type bearerHeader struct{ http.Header }

func (h bearerHeader) Keys() []string {
	keys := make([]string, 0, len(h.Header))
	for k := range h.Header {
		keys = append(keys, k)
	}
	return keys
}

func (b *bearerTransporter) Kind() ktransport.Kind            { return ktransport.KindHTTP }
func (b *bearerTransporter) Endpoint() string                 { return "" }
func (b *bearerTransporter) Operation() string                { return "/test.Probe/Op" }
func (b *bearerTransporter) RequestHeader() ktransport.Header { return bearerHeader{b.hdr} }
func (b *bearerTransporter) ReplyHeader() ktransport.Header   { return bearerHeader{http.Header{}} }

// callThroughAuthMiddleware 用真实的令牌校验器走一遍 REST 鉴权中间件（不做 casbin），
// 返回 handler 里拿到的操作人。
func callThroughAuthMiddleware(e *authSvcEnv, token string) (*authenticationV1.UserTokenPayload, error) {
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	ctx := ktransport.NewServerContext(context.Background(), &bearerTransporter{hdr: hdr})

	var operator *authenticationV1.UserTokenPayload
	mw := auth.Server(
		auth.WithAccessTokenChecker(data.NewTokenCheckerForTest(e.svc.authenticator)),
		auth.WithEnableAuthority(false),
	)
	_, err := mw(func(c context.Context, r interface{}) (interface{}, error) {
		var ferr error
		operator, ferr = auth.FromContext(c)
		return nil, ferr
	})(ctx, nil)
	return operator, err
}

func (e *authSvcEnv) issue(t *testing.T, ct authenticationV1.ClientType, userID uint32) (access, refresh string) {
	t.Helper()
	access, refresh, err := e.svc.authenticator.CreateUserToken(context.Background(), ct,
		&authenticationV1.UserTokenPayload{UserId: userID, Username: trans.Ptr(authSvcTestUsername)})
	require.NoError(t, err)
	return access, refresh
}

// app 令牌：能过 REST 中间件；SSE 握手、导出、刷新端点都拒绝。
func TestAppToken_SharedAndAdminOnlyChannels(t *testing.T) {
	e := newAuthenticationServiceForTest(t)
	appAT, _ := e.issue(t, authenticationV1.ClientType_app, authSvcTestUserID)
	adminAT, _ := e.issue(t, authenticationV1.ClientType_admin, authSvcTestUserID)
	uid := strconv.FormatUint(uint64(authSvcTestUserID), 10)

	// REST 中间件两类令牌都收，操作人带着各自的客户端类型。
	op, err := callThroughAuthMiddleware(e, appAT)
	require.NoError(t, err)
	require.Equal(t, authSvcTestUserID, op.GetUserId())
	require.Equal(t, authenticationV1.ClientType_app, op.GetClientType())
	op, err = callThroughAuthMiddleware(e, adminAT)
	require.NoError(t, err)
	require.Equal(t, authenticationV1.ClientType_admin, op.GetClientType())

	// SSE 握手只收 admin。
	im := &InternalMessageService{log: bLogger.NewHelper(bLogger.NopLogger()), authenticator: e.svc.authenticator}
	sseReq := httptest.NewRequest("GET", "/events?stream="+uid, nil)
	err = im.HandleAuthorize(sseReq, appAT)
	require.True(t, authenticationV1.IsUnauthorized(err), "app 令牌不应建立 SSE 连接: %v", err)
	require.NoError(t, im.HandleAuthorize(sseReq, adminAT))

	// 导出只收 admin。
	checker := data.NewTokenCheckerForTest(e.svc.authenticator)
	exportReq := httptest.NewRequest("GET", "/admin/v1/audit-logs:export", nil)
	exportReq.Header.Set("Authorization", "Bearer "+appAT)
	rec := httptest.NewRecorder()
	_, _, ok := authorizeExportRequest(rec, exportReq, checker)
	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	exportReq.Header.Set("Authorization", "Bearer "+adminAT)
	rec = httptest.NewRecorder()
	_, exportOp, ok := authorizeExportRequest(rec, exportReq, checker)
	require.True(t, ok)
	require.Equal(t, authSvcTestUserID, exportOp.GetUserId())

	// 拿 app 的 access token 当 refresh cookie：拒绝。
	hdr := http.Header{}
	hdr.Set("Cookie", "refresh_token="+appAT)
	resp, err := e.svc.RefreshToken(headerCtx(context.Background(), hdr),
		&authenticationV1.LoginRequest{GrantType: authenticationV1.GrantType_refresh_token, ClientType: authenticationV1.ClientType_app.Enum()})
	require.True(t, authenticationV1.IsIncorrectRefreshToken(err), "got %v", err)
	require.Nil(t, resp)
}

// 令牌失效时中间件返回 401 + reason TOKEN_EXPIRED。
func TestAuthMiddleware_TokenExpiredReason(t *testing.T) {
	e := newAuthenticationServiceForTest(t)
	appAT, _ := e.issue(t, authenticationV1.ClientType_app, authSvcTestUserID)
	require.NoError(t, e.svc.authenticator.RevokeUserToken(context.Background(), authenticationV1.ClientType_app, authSvcTestUserID))

	for name, token := range map[string]string{"已吊销": appAT, "不是 JWT": "garbage", "没带令牌": ""} {
		_, err := callThroughAuthMiddleware(e, token)
		se := kratosErrors.FromError(err)
		require.Equal(t, int32(401), se.Code, name)
		require.Equal(t, "TOKEN_EXPIRED", se.Reason, name)
	}
}

// 两个 Logout 各收各的令牌。
func TestLogout_EachAcceptsOnlyItsOwnTokenType(t *testing.T) {
	e := newAuthenticationServiceForTest(t)
	base := context.Background()
	adminAT, _ := e.issue(t, authenticationV1.ClientType_admin, authSvcTestUserID)
	appAT, _ := e.issue(t, authenticationV1.ClientType_app, authSvcTestUserID)

	appOp, err := callThroughAuthMiddleware(e, appAT)
	require.NoError(t, err)
	adminOp, err := callThroughAuthMiddleware(e, adminAT)
	require.NoError(t, err)

	// app 令牌调管理端 Logout：400，两边的会话都没动。
	_, err = e.svc.Logout(auth.NewContext(base, appOp), &emptypb.Empty{})
	require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)
	_, err = callThroughAuthMiddleware(e, adminAT)
	require.NoError(t, err)
	_, err = callThroughAuthMiddleware(e, appAT)
	require.NoError(t, err)

	// admin 令牌调小程序 Logout：400。
	miniapp := &MiniappAuthService{log: bLogger.NewHelper(bLogger.NopLogger())}
	_, err = miniapp.Logout(auth.NewContext(base, adminOp), &emptypb.Empty{})
	require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)
	_, err = miniapp.Logout(base, &emptypb.Empty{})
	require.True(t, authenticationV1.IsUnauthorized(err), "got %v", err)

	// admin 令牌调管理端 Logout：只吊销 admin 会话，app 会话还在。
	_, err = e.svc.Logout(auth.NewContext(base, adminOp), &emptypb.Empty{})
	require.NoError(t, err)
	_, err = callThroughAuthMiddleware(e, adminAT)
	require.Error(t, err)
	_, err = callThroughAuthMiddleware(e, appAT)
	require.NoError(t, err)
}

// 微信配置还没接入（等同 app_id 为空）：/admin/v1/login 带 client_type=app 被拒，不签发任何令牌。
func TestLogin_AppClientTypeRejectedWhenMiniappDisabled(t *testing.T) {
	e := newAuthenticationServiceForTest(t)
	permID := e.seedBackendAccessPermission(t)
	roleID := e.seedRole(t, nil, permissionV1.Role_SYSTEM, constants.PlatformAdminRoleCode, []uint32{permID}, nil, nil)
	e.seedCredential(t, nil, authSvcTestUserID, authSvcTestUsername, authenticationV1.UserCredential_ENABLED)
	e.seedStubUser(authSvcTestUserID, nil, authSvcTestUsername, identityV1.User_NORMAL, []uint32{roleID})

	ctx, req := e.loginHappyReq(t, context.Background(), authSvcTestUsername, constants.DefaultUserPassword)
	req.ClientType = authenticationV1.ClientType_app.Enum()
	resp, err := e.svc.Login(ctx, req)
	require.True(t, authenticationV1.IsMiniappUnavailable(err), "got %v", err)
	require.Nil(t, resp)
	require.Empty(t, e.svc.authenticator.GetAccessTokens(context.Background(), authenticationV1.ClientType_app, authSvcTestUserID))

	// 同一个验证码没被消耗，管理端登录照常。
	req.ClientType = authenticationV1.ClientType_admin.Enum()
	resp, err = e.svc.Login(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetAccessToken())
}

// 升级前格式的管理端会话（access token 没有 ct）：访问、刷新、轮换都正常。
func TestLegacyAdminSession_AccessRefreshRotate(t *testing.T) {
	e := newAuthenticationServiceForTest(t)
	base := context.Background()
	a := e.svc.authenticator
	admin := authenticationV1.ClientType_admin

	permID := e.seedBackendAccessPermission(t)
	roleID := e.seedRole(t, nil, permissionV1.Role_SYSTEM, constants.PlatformAdminRoleCode, []uint32{permID}, nil, nil)
	e.seedStubUser(authSvcTestUserID, nil, authSvcTestUsername, identityV1.User_NORMAL, []uint32{roleID})

	// refresh token 的格式没变。access token 换成不带 ct 的旧格式样本，覆盖同一个 Redis 键。
	payload := &authenticationV1.UserTokenPayload{UserId: authSvcTestUserID, Username: trans.Ptr(authSvcTestUsername)}
	_, refresh, err := a.CreateUserToken(base, admin, payload)
	require.NoError(t, err)
	legacyAT, err := a.AdminAuthenticator.CreateIdentity(authnEngine.AuthClaims{
		"sub": authSvcTestUsername,
		"uid": authSvcTestUserID,
		"tid": uint32(0),
		"jti": payload.GetJti(),
		"roc": []string{constants.PlatformAdminRoleCode},
		"ipa": true,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	e.mr.Set("at:0:"+strconv.FormatUint(uint64(authSvcTestUserID), 10)+":"+payload.GetJti(), legacyAT)

	// 访问：REST 中间件、SSE 握手都当它是 admin 令牌。
	op, err := callThroughAuthMiddleware(e, legacyAT)
	require.NoError(t, err)
	require.Equal(t, admin, op.GetClientType())
	require.True(t, op.GetIsPlatformAdmin())
	im := &InternalMessageService{log: bLogger.NewHelper(bLogger.NopLogger()), authenticator: a}
	sseReq := httptest.NewRequest("GET", "/events?stream="+strconv.FormatUint(uint64(authSvcTestUserID), 10), nil)
	require.NoError(t, im.HandleAuthorize(sseReq, legacyAT))

	// 刷新：换出带 ct=admin 的新令牌。
	refreshReq := &authenticationV1.LoginRequest{GrantType: authenticationV1.GrantType_refresh_token}
	hdr := http.Header{}
	hdr.Set("Cookie", "refresh_token="+refresh)
	rot, err := e.svc.RefreshToken(headerCtx(base, hdr), refreshReq)
	require.NoError(t, err)
	op, err = callThroughAuthMiddleware(e, rot.GetAccessToken())
	require.NoError(t, err)
	require.NotNil(t, op.ClientType)
	require.Equal(t, admin, op.GetClientType())

	// 旧 access token 在自己的有效期内继续可用（轮换不删旧 AT）。
	_, err = callThroughAuthMiddleware(e, legacyAT)
	require.NoError(t, err)

	// 再轮换一次：新会话的 refresh token 在 Redis 里，取出来续期。
	rts := a.GetRefreshTokensForTest(base, admin, authSvcTestUserID)
	require.Len(t, rts, 1)
	hdr.Set("Cookie", "refresh_token="+rts[0])
	rot2, err := e.svc.RefreshToken(headerCtx(base, hdr), refreshReq)
	require.NoError(t, err)
	_, err = callThroughAuthMiddleware(e, rot2.GetAccessToken())
	require.NoError(t, err)

	// 登出照常：全部 admin 令牌失效，包括旧格式那一枚。
	_, err = e.svc.Logout(auth.NewContext(base, op), &emptypb.Empty{})
	require.NoError(t, err)
	_, err = callThroughAuthMiddleware(e, legacyAT)
	require.Error(t, err)
	_, err = callThroughAuthMiddleware(e, rot2.GetAccessToken())
	require.Error(t, err)
}

// 令牌存储（Redis）读不出来时中间件返回 503，不是 TOKEN_EXPIRED：小程序不该把它当成续期信号。
func TestAuthMiddleware_TokenStoreFailureIs503(t *testing.T) {
	e := newAuthenticationServiceForTest(t)
	appAT, _ := e.issue(t, authenticationV1.ClientType_app, authSvcTestUserID)
	_, err := callThroughAuthMiddleware(e, appAT)
	require.NoError(t, err)

	e.mr.Close()
	_, err = callThroughAuthMiddleware(e, appAT)
	se := kratosErrors.FromError(err)
	require.Equal(t, int32(503), se.Code)
	require.NotEqual(t, "TOKEN_EXPIRED", se.Reason)
}
