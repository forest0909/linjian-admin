package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware/selector"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"go-wind-admin/third_party/go-utils/authorizer"
	"go-wind-admin/third_party/go-utils/trans"
	authz "go-wind-admin/third_party/kratos-authz/middleware"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/app/admin/service/internal/data"

	"go-wind-admin/pkg/middleware/auth"
)

// 权限（HTTP 层）：小程序令牌的内容和管理端一样，走同一条中间件，
// casbin、数据范围、字段屏蔽都原样生效；现有自助接口不需要额外授权。

// permUserServiceAdapter 和 rest_server.go 里的 userServiceServerAdapter 同形：
// 把 HTTP 服务接口桥接成 gRPC 服务接口，好套字段权限和脱敏两层装饰。
type permUserServiceAdapter struct {
	adminV1.UnsafeUserServiceServer
	adminV1.UserServiceHTTPServer
}

// permHTTP 一台进程内的 HTTP 服务：认证中间件加接口级鉴权（casbin），和 NewRestMiddleware 的顺序一致。
type permHTTP struct {
	srv *khttp.Server
	az  *authorizer.Authorizer
}

func newPermHTTP(t *testing.T, e *miniappEnv) *permHTTP {
	t.Helper()
	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())
	provider := data.NewAuthorizerProvider(boot, e.authn.roleRepo, data.NewApiRepoForTest(e.entClient))
	az := authorizer.NewAuthorizer(context.Background(), bLogger.NopLogger(), &authorizer.EngineConfig{Type: "casbin"}, provider)
	require.NoError(t, az.ResetPolicies(e.sys))

	// 只需登录、不走接口级鉴权的操作（rest_server.go 的 selfServiceOperations 里和本用例有关的几项；
	// 那张表本身由 server 包的 TestAuthzRequiredMatcher 覆盖）。
	selfService := map[string]bool{
		adminV1.OperationUserProfileServiceGetUser: true,
		adminV1.OperationMiniappAuthServiceLogout:  true,
	}
	whitelist := map[string]bool{
		adminV1.OperationMiniappAuthServiceLoginByPhone: true,
		adminV1.OperationMiniappAuthServiceSilentLogin:  true,
	}

	srv := khttp.NewServer(khttp.Middleware(
		selector.Server(
			auth.Server(
				auth.WithAccessTokenChecker(data.NewTokenCheckerForTest(e.authenticator)),
				auth.WithInjectMetadata(false),
				auth.WithInjectEnt(true),
			),
			selector.Server(authz.Server(az.Engine())).
				Match(func(_ context.Context, op string) bool { return !selfService[op] }).
				Build(),
		).
			Match(func(_ context.Context, op string) bool { return !whitelist[op] }).
			Build(),
	))
	adminV1.RegisterMiniappAuthServiceHTTPServer(srv, e.mini)
	adminV1.RegisterUserProfileServiceHTTPServer(srv, e.profile)
	adminV1.RegisterUserServiceHTTPServer(srv, adminV1.RedactedUserServiceServer(
		NewFieldPermissionUserServiceServer(&permUserServiceAdapter{UserServiceHTTPServer: e.users}), nil))
	return &permHTTP{srv: srv, az: az}
}

func (h *permHTTP) do(method, path, token string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
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

func TestMiniappHTTP_AppTokenPermissions(t *testing.T) {
	e := newMiniappEnv(t)
	seed := e.seed

	// 接口和权限点：用户列表（授权给角色）、绑定列表（先不授权）。
	usersAPI := seed.seedProbeAPI(t, "/admin/v1/users", "GET")
	bindingsAPI := seed.seedProbeAPI(t, "/admin/v1/users/{userId}/miniapp-bindings", "GET")
	createPerm := func(code string, apiID uint32) uint32 {
		require.NoError(t, e.authn.permissionRepo.Create(e.sys, &permissionV1.CreatePermissionRequest{
			Data: &permissionV1.Permission{Name: trans.Ptr(code), Code: trans.Ptr(code), Status: permissionV1.Permission_ON.Enum(), ApiIds: []uint32{apiID}},
		}))
		ids, err := e.authn.permissionRepo.GetPermissionIDsByCodes(e.sys, []string{code})
		require.NoError(t, err)
		require.Len(t, ids, 1)
		return ids[0]
	}
	usersPerm := createPerm("test:users:list", usersAPI)
	bindingsPerm := createPerm("test:miniapp-bindings:list", bindingsAPI)

	// 普通角色：访问后台 + 用户列表；数据范围只有本人；屏蔽 User.email。
	roleID := seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "field-staff", []uint32{e.permID, usersPerm},
		identityV1.DataScope_SELF.Enum(),
		[]*permissionV1.RoleFieldPermission{{Resource: "User", HiddenFields: []string{"email"}}})
	uid := e.seedUser("staff", "13800000001", roleID)
	e.client.User.UpdateOneID(uid).SetEmail("staff@example.com").ExecX(e.sys)
	other := e.seedUser("colleague", "13800000002", roleID)

	h := newPermHTTP(t, e)

	// 同一个用户、同一个角色：一个小程序令牌，一个管理端令牌。
	appResp, err := e.phoneLogin("10.0.0.1", "openid-staff-0001", "13800000001")
	require.NoError(t, err)
	adminResp, err := e.adminPasswordLogin("10.0.0.1", "staff", defaultPassword)
	require.NoError(t, err)
	tokens := map[string]string{"小程序令牌": appResp.GetAccessToken(), "管理端令牌": adminResp.GetAccessToken()}

	bindingsPath := fmt.Sprintf("/admin/v1/users/%d/miniapp-bindings", uid)
	for name, token := range tokens {
		// 有权的路由通过；数据范围外的用户查不到；被屏蔽的字段不出现。
		status, body := h.do(http.MethodGet, "/admin/v1/users", token)
		require.Equal(t, 200, status, "%s: %v", name, body)
		items, _ := body["items"].([]any)
		require.Len(t, items, 1, "%s: 数据范围只有本人", name)
		item := items[0].(map[string]any)
		require.Equal(t, "staff", item["username"], name)
		require.NotContains(t, item, "email", "%s: 字段屏蔽仍然生效", name)

		// 无权的路由 403。
		status, body = h.do(http.MethodGet, bindingsPath, token)
		require.Equal(t, 403, status, "%s: %v", name, body)

		// 现有自助接口不需要额外授权。
		status, body = h.do(http.MethodGet, "/admin/v1/me", token)
		require.Equal(t, 200, status, "%s: %v", name, body)
		require.Equal(t, "staff", body["username"], name)
	}

	// 没带令牌：401 TOKEN_EXPIRED。
	status, body := h.do(http.MethodGet, "/admin/v1/users", "")
	require.Equal(t, 401, status)
	require.Equal(t, "TOKEN_EXPIRED", body["reason"])

	// 给角色授权绑定列表接口后：两类令牌都通过；数据范围外的用户仍然 404。
	require.NoError(t, e.authn.roleRepo.Update(e.sys, &permissionV1.UpdateRoleRequest{
		Id:         roleID,
		Data:       &permissionV1.Role{Permissions: []uint32{e.permID, usersPerm, bindingsPerm}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"permissions"}},
	}))
	require.NoError(t, h.az.ResetPolicies(e.sys))
	for name, token := range tokens {
		status, body = h.do(http.MethodGet, bindingsPath, token)
		require.Equal(t, 200, status, "%s: %v", name, body)
		bound, _ := body["items"].([]any)
		require.Len(t, bound, 1, name)
		require.Equal(t, "openid****0001", bound[0].(map[string]any)["openidMasked"], name)

		status, _ = h.do(http.MethodGet, fmt.Sprintf("/admin/v1/users/%d/miniapp-bindings", other), token)
		require.Equal(t, 404, status, "%s: 数据范围外的用户", name)
	}

	// 小程序退出是自助接口：app 令牌不需要授权；管理端令牌调它 400。
	status, body = h.do(http.MethodPost, "/admin/v1/miniapp/logout", tokens["管理端令牌"])
	require.Equal(t, 400, status, "%v", body)
	status, body = h.do(http.MethodPost, "/admin/v1/miniapp/logout", tokens["小程序令牌"])
	require.Equal(t, 200, status, "%v", body)
	status, body = h.do(http.MethodGet, "/admin/v1/me", tokens["小程序令牌"])
	require.Equal(t, 401, status)
	require.Equal(t, "TOKEN_EXPIRED", body["reason"])
	status, _ = h.do(http.MethodGet, "/admin/v1/me", tokens["管理端令牌"])
	require.Equal(t, 200, status, "管理端会话不受小程序退出影响")
}
