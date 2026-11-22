package logging

import (
	nethttp "net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

// 登录审计的用户名不信任请求体（A345 审查第 7 条）。

// 一键登录、小程序退出的请求里本来没有 username：多塞一个不能进审计。
// 没有已验证身份时用户名留空；有身份时用身份里的用户名。
func TestLoginAudit_BodyUsernameIgnoredOutsidePasswordLogin(t *testing.T) {
	const body = `{"code":"c","phoneCode":"p","username":"attacker"}`
	for _, op := range []string{adminV1.OperationMiniappAuthServiceLoginByPhone, adminV1.OperationMiniappAuthServiceLogout} {
		env := newAuditServer(t)
		env.fire(nethttp.MethodPost, "/case/5", map[string]string{
			"X-Test-Operation": op, "Content-Type": "application/json",
		}, body, "127.0.0.1:1234")
		env.fire(nethttp.MethodPost, "/case/5", map[string]string{
			"X-Test-Operation": op, "Content-Type": "application/json", "X-Test-Verified": "1",
		}, body, "127.0.0.1:1234")

		require.Len(t, env.capture.login, 2, op)
		assert.Empty(t, env.capture.login[0].GetUsername(), "%s：匿名时用户名留空", op)
		assert.Zero(t, env.capture.login[0].GetUserId(), op)
		assert.Equal(t, "alice", env.capture.login[1].GetUsername(), "%s：用已验证身份的用户名", op)
		assert.Equal(t, uint32(42), env.capture.login[1].GetUserId(), op)
	}
}

// 账号密码登录在服务端还没交出用户名之前就失败（比如验证码不对）时，用户名取请求体；
// 填的是手机号就打码，普通用户名原样。
func TestLoginAudit_PasswordLoginBodyUsernameMasksMobile(t *testing.T) {
	env := newAuditServer(t)
	fire := func(username string) {
		env.fire(nethttp.MethodPost, "/case/5", map[string]string{
			"X-Test-Operation": adminV1.OperationAuthenticationServiceLogin, "Content-Type": "application/json",
		}, `{"username":"`+username+`","password":"x"}`, "127.0.0.1:1234")
	}
	fire("13800001234")
	fire("bob")

	require.Len(t, env.capture.login, 2)
	assert.Equal(t, "138****1234", env.capture.login[0].GetUsername())
	assert.Equal(t, "bob", env.capture.login[1].GetUsername())
}

// 访问日志：登录请求的 username 是手机号时打码，普通用户名原样。
func TestFormatArgs_MasksMobileLoginIdentifier(t *testing.T) {
	req := func(username string) *authenticationV1.LoginRequest {
		return &authenticationV1.LoginRequest{Identifier: &authenticationV1.LoginRequest_Username{Username: username}}
	}
	out := FormatArgs(req("13800001234"))
	assert.NotContains(t, out, "13800001234")
	assert.Contains(t, out, "138****1234")

	assert.Contains(t, FormatArgs(req("zhangsan")), "zhangsan")
}
