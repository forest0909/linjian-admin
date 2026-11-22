package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/third_party/go-utils/trans"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	auditV1 "go-wind-admin/api/gen/go/audit/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
)

// A345 审查修复的服务层用例（第 1、2、3、7 条）。第 4、5、6 条的用例在 data 包。

// ---- 第 1 条：管理员强制下线 app 会话要过数据范围和权限上限 ----

func (h *permHTTP) doBody(method, path, token, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:4321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// 只授权了「强制下线」接口的管理员：目标在数据范围外、或目标权限比自己高时，
// 不能靠强制下线 app 会话删掉目标的微信绑定；绑定和令牌都保留。范围和上限都满足时照常下线。
func TestMiniappHTTP_ForceLogoutAppSessionRespectsScopeAndCeiling(t *testing.T) {
	e := newMiniappEnv(t)
	seed := e.seed

	createPerm := func(code string, apiID uint32) uint32 {
		require.NoError(t, e.authn.permissionRepo.Create(e.sys, &permissionV1.CreatePermissionRequest{
			Data: &permissionV1.Permission{Name: trans.Ptr(code), Code: trans.Ptr(code), Status: permissionV1.Permission_ON.Enum(), ApiIds: []uint32{apiID}},
		}))
		ids, err := e.authn.permissionRepo.GetPermissionIDsByCodes(e.sys, []string{code})
		require.NoError(t, err)
		require.Len(t, ids, 1)
		return ids[0]
	}
	forcePerm := createPerm("test:online-session:force-logout", seed.seedProbeAPI(t, "/admin/v1/online-session/force-logout", "POST"))
	extraPerm := createPerm("test:users:list", seed.seedProbeAPI(t, "/admin/v1/users", "GET"))

	all, self := identityV1.DataScope_ALL.Enum(), identityV1.DataScope_SELF.Enum()
	targetRole := seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "fl-target", []uint32{e.permID, extraPerm}, self, nil)
	scopedRole := seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "fl-scoped", []uint32{e.permID, forcePerm, extraPerm}, self, nil)
	lowRole := seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "fl-low", []uint32{e.permID, forcePerm}, all, nil)
	okRole := seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "fl-ok", []uint32{e.permID, forcePerm, extraPerm}, all, nil)

	target := e.seedUser("fl-target-user", "13800000001", targetRole)
	e.seedUser("fl-scoped-op", "13800000002", scopedRole)
	e.seedUser("fl-low-op", "13800000003", lowRole)
	e.seedUser("fl-ok-op", "13800000004", okRole)

	h := newPermHTTP(t, e)
	adminV1.RegisterOnlineSessionServiceHTTPServer(h.srv, e.online)
	require.NoError(t, h.az.ResetPolicies(e.sys))

	appResp, err := e.phoneLogin("10.0.0.1", "openid-fl-0001", "13800000001")
	require.NoError(t, err)
	appToken := appResp.GetAccessToken()
	body := fmt.Sprintf(`{"clientType":"app","userId":%d,"jti":%q}`, target, e.payload(appToken).GetJti())

	opToken := func(username string) string {
		resp, lerr := e.adminPasswordLogin("10.0.0.9", username, defaultPassword)
		require.NoError(t, lerr)
		return resp.GetAccessToken()
	}
	requireUntouched := func(msg string) {
		require.Len(t, e.bindings(target), 1, "%s：绑定保留", msg)
		require.True(t, e.tokenValid(appToken), "%s：令牌保留", msg)
	}

	// 目标在数据范围外：404。
	status, out := h.doBody(http.MethodPost, "/admin/v1/online-session/force-logout", opToken("fl-scoped-op"), body)
	require.Equal(t, 404, status, "%v", out)
	requireUntouched("数据范围外")

	// 目标权限比操作人高：403。
	status, out = h.doBody(http.MethodPost, "/admin/v1/online-session/force-logout", opToken("fl-low-op"), body)
	require.Equal(t, 403, status, "%v", out)
	require.Equal(t, "FORBIDDEN", out["reason"])
	requireUntouched("权限上限不够")

	// 没有接口权限的人（目标自己的管理端令牌没有强制下线权限）：接口级 403，同样什么都没动。
	status, _ = h.doBody(http.MethodPost, "/admin/v1/online-session/force-logout", opToken("fl-target-user"), body)
	require.Equal(t, 403, status)
	requireUntouched("没有接口权限")

	// 范围和上限都满足：下线，绑定删除，令牌失效。
	status, out = h.doBody(http.MethodPost, "/admin/v1/online-session/force-logout", opToken("fl-ok-op"), body)
	require.Equal(t, 200, status, "%v", out)
	require.Empty(t, e.bindings(target))
	require.False(t, e.tokenValid(appToken))
}

// ---- 第 2 条：Redis 部分失败后，按会话下线的三个入口原样重试都能做完全量吊销 ----

func TestMiniappOffline_SessionEntriesRetryAfterPartialRedisFailure(t *testing.T) {
	entries := map[string]func(f *offlineFixture, opCtxA context.Context) error{
		"小程序退出": func(f *offlineFixture, opCtxA context.Context) error {
			_, err := f.mini.Logout(opCtxA, &emptypb.Empty{})
			return err
		},
		"自助踢 app 会话": func(f *offlineFixture, _ context.Context) error {
			_, err := f.online.RevokeMyOnlineSession(f.operatorOf(f.adminToken), revokeMyReq(authenticationV1.ClientType_app, f.jtiA))
			return err
		},
		"管理员强制下线": func(f *offlineFixture, _ context.Context) error {
			_, err := f.online.ForceLogoutSession(opCtx(f.sys, 9001, 0, true), forceLogoutReq(authenticationV1.ClientType_app, f.uid, f.jtiA))
			return err
		},
	}
	for name, call := range entries {
		t.Run(name, func(t *testing.T) {
			f := newOfflineFixture(t)
			opCtxA := f.operatorOf(f.tokenA)

			// 只让删 app access token 的 DEL 失败。
			f.fault.set(func(cmd string, args []string) bool {
				return cmd == "del" && len(args) > 1 && strings.HasPrefix(args[1], "at:1:")
			})
			requireReason(t, call(f, opCtxA), 503, "SERVICE_UNAVAILABLE")
			rows := f.bindings(f.uid)
			require.Len(t, rows, 1, "数据库部分已经提交")
			require.Equal(t, f.bindingB, rows[0].ID)
			require.True(t, f.tokenValid(f.tokenA), "令牌没删掉")
			require.True(t, f.tokenValid(f.tokenB), "另一个 app 会话的令牌也没删掉")

			// Redis 恢复，原样重试：成功，两个旧令牌都失效。
			f.fault.set(nil)
			require.NoError(t, call(f, opCtxA))
			require.False(t, f.tokenValid(f.tokenA), "原会话的旧令牌失效")
			require.False(t, f.tokenValid(f.tokenB), "同用户另一个 app 会话的旧令牌也失效")
			require.Empty(t, f.appTokens(f.uid))

			// 被下线的微信续不上；另一个微信的绑定还在，能续期。管理端会话不受影响。
			_, err := f.silentLogin("10.0.0.1", offlineOpenIDA)
			requireReason(t, err, 403, "MINIAPP_NOT_BOUND")
			resp, err := f.silentLogin("10.0.0.2", offlineOpenIDB)
			require.NoError(t, err)
			require.True(t, f.tokenValid(resp.GetAccessToken()))
			require.True(t, f.tokenValid(f.adminToken))
		})
	}
}

// ---- 第 3 条：撤销版本没推进成功时，在途的续期不能给已删的绑定签出新令牌 ----

// 续期读完版本和绑定之后停住；这时解绑的数据库部分提交了，但推进撤销版本的 INCR 失败
// （删令牌成功，解绑返回 503）。续期接着往下走：版本还是旧的，对得上，
// 但签发前在用户行锁下重新确认绑定，绑定已经不在，不写令牌。
func TestMiniappSilentLogin_NoTokenForUnboundBindingWhenVersionBumpFails(t *testing.T) {
	f := newOfflineFixture(t)

	// 续期走到「读完版本、第二次确认过绑定」的状态。
	version, err := f.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, f.uid)
	require.NoError(t, err)
	binding, err := f.credRepo.FindMiniappBinding(context.Background(), offlineOpenIDA)
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, f.bindingA, binding.ID)

	// 并发的解绑：只有 INCR 失败。
	f.fault.set(func(cmd string, args []string) bool {
		return cmd == "incr" && len(args) > 1 && args[1] == fmt.Sprintf("rv:1:%d", f.uid)
	})
	_, err = f.mini.DeleteUserBinding(opCtx(f.sys, 9001, 0, true), &authenticationV1.DeleteMiniappBindingRequest{UserId: f.uid, Id: f.bindingA})
	requireReason(t, err, 503, "SERVICE_UNAVAILABLE")
	f.fault.set(nil)
	after, err := f.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, f.uid)
	require.NoError(t, err)
	require.Equal(t, version, after, "版本没有推进：光靠版本条件拦不住")
	require.Empty(t, f.appTokens(f.uid), "旧令牌已经删掉")

	// 续期继续：签发被拒，没有新令牌落库。
	user, err := f.mini.loadUserForIssue(f.authn.resetContextForLogin(f.reqCtx("10.0.0.1")), f.uid)
	require.NoError(t, err)
	_, err = f.mini.issue(f.authn.resetContextForLogin(f.reqCtx("10.0.0.1")), user, binding.ID, version, false)
	requireReason(t, err, 409, "CONFLICT")
	require.Empty(t, f.appTokens(f.uid), "没有新令牌落库")

	// 另一个微信的绑定还在：同样的旧版本可以签发。
	_, err = f.mini.issue(f.authn.resetContextForLogin(f.reqCtx("10.0.0.2")), user, f.bindingB, version, false)
	require.NoError(t, err)
	require.Len(t, f.appTokens(f.uid), 1)
}

// 密码登录同理：密码校验通过之后密码被改掉（改密的数据库部分已提交、版本没推进），不签发。
func TestAppPasswordLogin_NoTokenWhenPasswordChangedAndVersionBumpFails(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	ctx := e.authn.resetContextForLogin(e.reqCtx("10.0.0.1"))

	version, err := e.authenticator.ReadRevokeVersion(context.Background(), authenticationV1.ClientType_app, uid)
	require.NoError(t, err)
	stale := "$2a$10$stale-hash-from-before-the-change"
	_, err = e.authn.issueAppSession(ctx, &authenticationV1.UserTokenPayload{UserId: uid}, 0, version, &stale)
	requireReason(t, err, 409, "CONFLICT")
	require.Empty(t, e.appTokens(uid))
}

// ---- 第 7 条：登录审计不信任请求体里的用户名，手机号不落原文 ----

func (h *miniappHTTP) postWithHeaders(path, body string, headers map[string]string) (int, map[string]any, http.Header) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:4321"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func TestMiniappHTTP_LoginAuditDoesNotTrustBodyUsername(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	h := newMiniappHTTP(e)
	adminV1.RegisterAuthenticationServiceHTTPServer(h.srv, e.authn)

	const matchedMobile, unmatchedMobile = "13800000001", "13900001234"

	// 一键登录：请求体里多塞一个 username（HTTP 解码容许未知字段）。
	status, body, _ := h.post("/admin/v1/miniapp/login/phone", fmt.Sprintf(`{"code":%q,"phoneCode":%q,"username":"attacker"}`,
		e.wxCode("openid-audit-0001"), e.phoneCode(matchedMobile)))
	require.Equal(t, 200, status, "%v", body)
	status, _, _ = h.post("/admin/v1/miniapp/login/phone", fmt.Sprintf(`{"code":%q,"phoneCode":%q,"username":"attacker"}`,
		e.wxCode("openid-audit-0002"), e.phoneCode("13800009999")))
	require.Equal(t, 403, status)

	// 小程序账号密码登录，username 填手机号。
	passwordLogin := func(username, plain string) (int, map[string]any, http.Header) {
		id, _, answer, err := e.captcha.Generate()
		require.NoError(t, err)
		require.NoError(t, e.captcha.Save(context.Background(), id, answer))
		return h.postWithHeaders("/admin/v1/login",
			fmt.Sprintf(`{"grant_type":"password","username":%q,"password":%q,"client_type":"app"}`, username, encryptLoginPassword(t, plain)),
			map[string]string{"X-Captcha-Id": id, "X-Captcha-Value": answer})
	}
	status, body, header := passwordLogin(matchedMobile, "Wrong@Password1")
	require.NotEqual(t, 200, status, "%v", body)
	require.Empty(t, header.Get("X-Audit-Username"), "匹配到的用户名不能经响应头漏给调用方")
	status, body, header = passwordLogin(unmatchedMobile, "Wrong@Password1")
	require.NotEqual(t, 200, status, "%v", body)
	require.Empty(t, header.Get("X-Audit-Username"))
	status, body, _ = passwordLogin(matchedMobile, defaultPassword)
	require.Equal(t, 200, status, "%v", body)

	require.Len(t, h.logins, 5)
	// 一键登录成功：真实用户名，不是请求体里的。
	require.Equal(t, auditV1.LoginAuditLog_SUCCESS, h.logins[0].GetStatus())
	require.Equal(t, uid, h.logins[0].GetUserId())
	require.Equal(t, "zhangsan", h.logins[0].GetUsername())
	// 一键登录失败、没匹配到用户：用户名留空。
	require.Equal(t, auditV1.LoginAuditLog_FAILED, h.logins[1].GetStatus())
	require.Empty(t, h.logins[1].GetUsername())
	// 密码登录失败、手机号匹配到账号：记解析出的用户名。
	require.Equal(t, auditV1.LoginAuditLog_FAILED, h.logins[2].GetStatus())
	require.Equal(t, "zhangsan", h.logins[2].GetUsername())
	// 密码登录失败、手机号没匹配到：打码。
	require.Equal(t, auditV1.LoginAuditLog_FAILED, h.logins[3].GetStatus())
	require.Equal(t, "139****1234", h.logins[3].GetUsername())
	// 密码登录成功。
	require.Equal(t, auditV1.LoginAuditLog_SUCCESS, h.logins[4].GetStatus())
	require.Equal(t, "zhangsan", h.logins[4].GetUsername())

	for i, rec := range h.logins {
		raw, err := protojson.Marshal(rec)
		require.NoError(t, err)
		for _, secret := range []string{matchedMobile, unmatchedMobile, "13800009999", "attacker"} {
			require.NotContains(t, string(raw), secret, "第 %d 条登录审计", i)
		}
	}
	// 服务日志（含凭证校验失败的那一行）里也没有完整手机号。
	e.requireLogsClean(matchedMobile, unmatchedMobile, "13800009999")
}
