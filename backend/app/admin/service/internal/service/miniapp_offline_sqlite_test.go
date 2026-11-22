package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/third_party/go-crud/viewer"
	"go-wind-admin/third_party/go-utils/trans"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	onlineSessionV1 "go-wind-admin/api/gen/go/online_session/service/v1"
)

// ---- 下线：四个入口走同一个函数 ----

// offlineFixture 一个用户、两个微信（A、B）各有绑定和会话，外加一个管理端会话。
type offlineFixture struct {
	*miniappEnv
	uid                uint32
	tokenA, tokenB     string
	jtiA               string
	bindingA, bindingB uint32
	adminToken         string
}

const (
	offlineMobile  = "13800000001"
	offlineOpenIDA = "openid-offline-AAAA"
	offlineOpenIDB = "openid-offline-BBBB"
)

func newOfflineFixture(t *testing.T) *offlineFixture {
	e := newMiniappEnv(t)
	f := &offlineFixture{miniappEnv: e}
	f.uid = e.seedUser("zhangsan", offlineMobile)

	respA, err := e.phoneLogin("10.0.0.1", offlineOpenIDA, offlineMobile)
	require.NoError(t, err)
	respB, err := e.phoneLogin("10.0.0.2", offlineOpenIDB, offlineMobile)
	require.NoError(t, err)
	f.tokenA, f.tokenB = respA.GetAccessToken(), respB.GetAccessToken()
	f.jtiA = e.payload(f.tokenA).GetJti()
	for _, row := range e.bindings(f.uid) {
		switch *row.ProviderAccountID {
		case offlineOpenIDA:
			f.bindingA = row.ID
		case offlineOpenIDB:
			f.bindingB = row.ID
		}
	}
	require.NotZero(t, f.bindingA)
	require.NotZero(t, f.bindingB)
	f.adminToken = f.miniappEnv.adminToken(f.uid)
	e.revokedTypes() // 清掉准备阶段的记录
	return f
}

func revokeMyReq(ct authenticationV1.ClientType, jti string) *onlineSessionV1.RevokeMyOnlineSessionRequest {
	return &onlineSessionV1.RevokeMyOnlineSessionRequest{ClientType: ct.Enum(), Jti: trans.Ptr(jti)}
}

func forceLogoutReq(ct authenticationV1.ClientType, userID uint32, jti string) *onlineSessionV1.ForceLogoutSessionRequest {
	return &onlineSessionV1.ForceLogoutSessionRequest{ClientType: ct.Enum(), UserId: trans.Ptr(userID), Jti: trans.Ptr(jti)}
}

// requireAOffline 微信 A 被下线之后可观察的结果。
func (f *offlineFixture) requireAOffline(t *testing.T) {
	t.Helper()
	// 对应的绑定被删，另一条还在。
	rows := f.bindings(f.uid)
	require.Len(t, rows, 1)
	require.Equal(t, f.bindingB, rows[0].ID)

	// 该用户的 app 令牌全部失效。
	require.False(t, f.tokenValid(f.tokenA))
	require.False(t, f.tokenValid(f.tokenB))
	require.Empty(t, f.appTokens(f.uid))

	// 被下线的微信续期被拒，必须重新登录。
	_, err := f.silentLogin("10.0.0.1", offlineOpenIDA)
	requireReason(t, err, 403, "MINIAPP_NOT_BOUND")

	// 同一用户的另一个微信绑定还在，能静默续期。
	resp, err := f.silentLogin("10.0.0.2", offlineOpenIDB)
	require.NoError(t, err)
	require.True(t, f.tokenValid(resp.GetAccessToken()))

	// 管理端会话不受影响；全量吊销钩子只收到 app（SSE 只在 admin 吊销时断流）。
	require.True(t, f.tokenValid(f.adminToken))
	require.Equal(t, []authenticationV1.ClientType{authenticationV1.ClientType_app}, f.revokedTypes())
}

func TestMiniappOffline_FourEntries(t *testing.T) {
	t.Run("小程序退出", func(t *testing.T) {
		f := newOfflineFixture(t)
		_, err := f.mini.Logout(f.operatorOf(f.tokenA), &emptypb.Empty{})
		require.NoError(t, err)
		f.requireAOffline(t)
	})

	t.Run("自助踢 app 会话", func(t *testing.T) {
		f := newOfflineFixture(t)
		// 用户在管理端的个人中心踢掉自己的小程序会话。
		_, err := f.online.RevokeMyOnlineSession(f.operatorOf(f.adminToken), revokeMyReq(authenticationV1.ClientType_app, f.jtiA))
		require.NoError(t, err)
		f.requireAOffline(t)
	})

	t.Run("管理员强制下线 app 会话", func(t *testing.T) {
		f := newOfflineFixture(t)
		_, err := f.online.ForceLogoutSession(opCtx(f.sys, 9001, 0, true), forceLogoutReq(authenticationV1.ClientType_app, f.uid, f.jtiA))
		require.NoError(t, err)
		f.requireAOffline(t)
	})

	t.Run("管理端解绑", func(t *testing.T) {
		f := newOfflineFixture(t)
		_, err := f.mini.DeleteUserBinding(opCtx(f.sys, 9001, 0, true), &authenticationV1.DeleteMiniappBindingRequest{
			UserId: f.uid, Id: f.bindingA,
		})
		require.NoError(t, err)
		f.requireAOffline(t)
	})
}

// 目标是 admin 会话时，自助踢会话和强制下线保持原有行为：只吊销那一个会话，不动绑定和 app 会话。
func TestMiniappOffline_AdminSessionKeepsOldBehavior(t *testing.T) {
	f := newOfflineFixture(t)
	adminJti := f.payload(f.adminToken).GetJti()
	second := f.miniappEnv.adminToken(f.uid)

	_, err := f.online.ForceLogoutSession(opCtx(f.sys, 9001, 0, true), forceLogoutReq(authenticationV1.ClientType_admin, f.uid, adminJti))
	require.NoError(t, err)

	require.False(t, f.tokenValid(f.adminToken))
	require.True(t, f.tokenValid(second), "别的管理端会话还在")
	require.True(t, f.tokenValid(f.tokenA))
	require.True(t, f.tokenValid(f.tokenB))
	require.Len(t, f.bindings(f.uid), 2)
	require.Empty(t, f.revokedTypes(), "按 JTI 吊销不是全量吊销")
}

// H5 会话（BindingID 为 0）退出只吊销当前令牌，不删任何绑定，别的 app 会话还在。
func TestMiniappOffline_H5SessionOnlyRevokesItself(t *testing.T) {
	f := newOfflineFixture(t)
	resp, err := f.passwordLogin("10.0.0.3", "zhangsan", defaultPassword, "")
	require.NoError(t, err)
	require.False(t, resp.GetMiniappBound())
	h5Token := resp.GetAccessToken()
	h5Jti := f.payload(h5Token).GetJti()

	_, err = f.mini.Logout(f.operatorOf(h5Token), &emptypb.Empty{})
	require.NoError(t, err)

	require.False(t, f.tokenValid(h5Token))
	require.True(t, f.tokenValid(f.tokenA))
	require.True(t, f.tokenValid(f.tokenB))
	require.Len(t, f.bindings(f.uid), 2)
	meta, err := f.authenticator.GetSessionMeta(context.Background(), authenticationV1.ClientType_app, f.uid, h5Jti)
	require.NoError(t, err)
	require.Nil(t, meta, "会话元数据随令牌清掉")

	// 自助踢一个 H5 会话也一样。
	resp, err = f.passwordLogin("10.0.0.3", "zhangsan", defaultPassword, "")
	require.NoError(t, err)
	h5Token = resp.GetAccessToken()
	_, err = f.online.RevokeMyOnlineSession(f.operatorOf(f.adminToken), revokeMyReq(authenticationV1.ClientType_app, f.payload(h5Token).GetJti()))
	require.NoError(t, err)
	require.False(t, f.tokenValid(h5Token))
	require.True(t, f.tokenValid(f.tokenA))
	require.Len(t, f.bindings(f.uid), 2)
}

// 两个 Logout 各收各的令牌：admin 令牌调小程序 Logout 被拒，什么都没动。
func TestMiniappLogout_RejectsAdminToken(t *testing.T) {
	f := newOfflineFixture(t)
	_, err := f.mini.Logout(f.operatorOf(f.adminToken), &emptypb.Empty{})
	requireReason(t, err, 400, "BAD_REQUEST")
	require.True(t, f.tokenValid(f.tokenA))
	require.True(t, f.tokenValid(f.adminToken))
	require.Len(t, f.bindings(f.uid), 2)
}

// ---- 管理端的两个绑定接口 ----

func TestMiniappBindings_ListAndDeletePermissions(t *testing.T) {
	f := newOfflineFixture(t)
	admin := opCtx(f.sys, 9001, 0, true)

	// 列表：openid 只有打码后的样子（前 6 后 4），带绑定时间。
	list, err := f.mini.ListUserBindings(admin, &authenticationV1.ListMiniappBindingsRequest{UserId: f.uid})
	require.NoError(t, err)
	require.Len(t, list.GetItems(), 2)
	require.Equal(t, f.bindingA, list.GetItems()[0].GetId())
	require.Equal(t, "openid****AAAA", list.GetItems()[0].GetOpenidMasked())
	require.Equal(t, "openid****BBBB", list.GetItems()[1].GetOpenidMasked())
	require.NotNil(t, list.GetItems()[0].GetCreatedAt())

	// 没有登录身份。
	_, err = f.mini.ListUserBindings(context.Background(), &authenticationV1.ListMiniappBindingsRequest{UserId: f.uid})
	requireReason(t, err, 401, "UNAUTHORIZED")

	// 数据范围外：404，查不到也删不了。操作人只看得到单元 1 的用户，目标不在里面。
	require.NoError(t, f.client.OrgUnit.Create().SetID(1).SetName("u1").SetCode("MB1").SetPath("/1/").Exec(f.sys))
	scoped := operatorCtx(9002, viewer.DataScope{ScopeType: viewer.ScopeTypeUnit, TargetIDs: []uint64{1}})
	_, err = f.mini.ListUserBindings(scoped, &authenticationV1.ListMiniappBindingsRequest{UserId: f.uid})
	requireReason(t, err, 404, "NOT_FOUND")
	_, err = f.mini.DeleteUserBinding(scoped, &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA})
	requireReason(t, err, 404, "NOT_FOUND")

	// 权限上限不够：目标用户的角色权限比操作人多，解绑被拒。
	roles := &userServiceEnv{svc: f.users, ctx: f.sys}
	lowRole := roles.seedRoleWithPerms(t, "MB_LOW", []uint32{11})
	bigRole := roles.seedRoleWithPerms(t, "MB_BIG", []uint32{11, 22})
	operator := f.seedUser("low-operator", "13700000007", lowRole)
	f.client.UserRole.Create().SetUserID(f.uid).SetRoleID(bigRole).SaveX(f.sys)
	_, err = f.mini.DeleteUserBinding(opCtx(f.sys, operator, 0, false), &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA})
	requireReason(t, err, 403, "FORBIDDEN")
	// 看还是可以看的（列表没有权限上限的要求）。
	_, err = f.mini.ListUserBindings(opCtx(f.sys, operator, 0, false), &authenticationV1.ListMiniappBindingsRequest{UserId: f.uid})
	require.NoError(t, err)

	require.Len(t, f.bindings(f.uid), 2, "被拒的请求没有删任何绑定")
	require.True(t, f.tokenValid(f.tokenA), "也没有吊销令牌")

	// 拿别的用户的绑定 ID 来删：返回成功，没有任何绑定被删。
	other := f.seedUser("other", "13800000002")
	_, err = f.phoneLogin("10.0.0.9", "openid-other-CCCC", "13800000002")
	require.NoError(t, err)
	otherBinding := f.bindings(other)[0].ID
	_, err = f.mini.DeleteUserBinding(admin, &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: otherBinding})
	require.NoError(t, err)
	require.Len(t, f.bindings(other), 1, "别的用户的绑定不受影响")
	require.Len(t, f.bindings(f.uid), 2, "路径上这个用户的绑定也没少")

	// 解绑是幂等的：已经不存在的绑定再删一次仍然成功。
	_, err = f.mini.DeleteUserBinding(admin, &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA})
	require.NoError(t, err)
	_, err = f.mini.DeleteUserBinding(admin, &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA})
	require.NoError(t, err)
	require.Len(t, f.bindings(f.uid), 1)

	f.requireLogsClean(offlineOpenIDA, offlineOpenIDB)
}

// ---- Redis 部分失败 ----

// 推进撤销版本失败：接口返回 503；数据库里绑定已删；再调一次成功。
func TestMiniappOffline_RevokeVersionBumpFails(t *testing.T) {
	f := newOfflineFixture(t)
	admin := opCtx(f.sys, 9001, 0, true)
	req := &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA}

	f.fault.set(func(name string, args []string) bool {
		return name == "incr" && len(args) > 1 && args[1] == fmt.Sprintf("rv:1:%d", f.uid)
	})
	_, err := f.mini.DeleteUserBinding(admin, req)
	requireReason(t, err, 503, "SERVICE_UNAVAILABLE")
	rows := f.bindings(f.uid)
	require.Len(t, rows, 1, "数据库部分已经提交")
	require.Equal(t, f.bindingB, rows[0].ID)
	// 绑定没了，这个微信续不上。
	_, err = f.silentLogin("10.0.0.1", offlineOpenIDA)
	requireReason(t, err, 403, "MINIAPP_NOT_BOUND")

	f.fault.set(nil)
	_, err = f.mini.DeleteUserBinding(admin, req)
	require.NoError(t, err, "解绑已提交后重试：返回成功")
	require.False(t, f.tokenValid(f.tokenA))
}

// 版本推进成功但删除令牌失败：接口返回 503；此时旧令牌仍能访问；再调一次后旧令牌 401。
func TestMiniappOffline_TokenDeletionFails(t *testing.T) {
	f := newOfflineFixture(t)
	admin := opCtx(f.sys, 9001, 0, true)
	req := &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA}
	// 走一遍真实的鉴权中间件。
	mw := &authSvcEnv{svc: f.authn}

	version, err := f.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, f.uid)
	require.NoError(t, err)

	f.fault.set(func(name string, args []string) bool {
		return name == "del" && len(args) > 1 && len(args[1]) > 5 && args[1][:5] == "at:1:"
	})
	_, err = f.mini.DeleteUserBinding(admin, req)
	requireReason(t, err, 503, "SERVICE_UNAVAILABLE")

	// 版本已经推进：在途的签发写不进去。
	after, err := f.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, f.uid)
	require.NoError(t, err)
	require.Equal(t, version+1, after)
	// 但没删掉的旧令牌在自己的有效期内还能访问（残留风险，见设计方案 12.1）。
	op, err := callThroughAuthMiddleware(mw, f.tokenA)
	require.NoError(t, err)
	require.Equal(t, f.uid, op.GetUserId())
	// 绑定已删，这些令牌过期后续不上。
	require.Len(t, f.bindings(f.uid), 1)

	// 再调一次：旧令牌 401。
	f.fault.set(nil)
	_, err = f.mini.DeleteUserBinding(admin, req)
	require.NoError(t, err)
	_, err = callThroughAuthMiddleware(mw, f.tokenA)
	requireReason(t, err, 401, "TOKEN_EXPIRED")
	_, err = callThroughAuthMiddleware(mw, f.tokenB)
	requireReason(t, err, 401, "TOKEN_EXPIRED")
}

// 小程序退出时 Redis 删除失败：503；绑定已删；用还没失效的令牌再退出一次，令牌失效。
func TestMiniappLogout_RetryAfterRedisFailure(t *testing.T) {
	f := newOfflineFixture(t)
	opCtxA := f.operatorOf(f.tokenA)

	f.fault.set(func(name string, args []string) bool {
		return name == "del" && len(args) > 1 && len(args[1]) > 5 && args[1][:5] == "at:1:"
	})
	_, err := f.mini.Logout(opCtxA, &emptypb.Empty{})
	requireReason(t, err, 503, "SERVICE_UNAVAILABLE")
	require.True(t, f.tokenValid(f.tokenA), "令牌没删掉")
	require.Len(t, f.bindings(f.uid), 1, "绑定已删")

	f.fault.set(nil)
	_, err = f.mini.Logout(opCtxA, &emptypb.Empty{})
	require.NoError(t, err)
	require.False(t, f.tokenValid(f.tokenA))
}
