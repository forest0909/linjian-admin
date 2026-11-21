package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware/validate"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	auditV1 "go-wind-admin/api/gen/go/audit/service/v1"

	"go-wind-admin/pkg/middleware/logging"
)

// miniappHTTP 把小程序登录接口挂到一台进程内的 kratos HTTP 服务上，外面套审计中间件。
// 用来验证线上的 JSON 形态和登录审计的内容。
type miniappHTTP struct {
	srv    *khttp.Server
	logins []*auditV1.LoginAuditLog
	ops    []*auditV1.OperationAuditLog
}

func newMiniappHTTP(e *miniappEnv) *miniappHTTP {
	h := &miniappHTTP{}
	// 顺序和 NewRestMiddleware 一致：审计在外，入参校验在内。
	h.srv = khttp.NewServer(khttp.Middleware(
		logging.Server(
			logging.WithWriteLoginLogFunc(func(_ context.Context, d *auditV1.LoginAuditLog) error {
				h.logins = append(h.logins, d)
				return nil
			}),
			logging.WithWriteOperationAuditLogFunc(func(_ context.Context, d *auditV1.OperationAuditLog) error {
				h.ops = append(h.ops, d)
				return nil
			}),
		),
		validate.Validator(),
	))
	adminV1.RegisterMiniappAuthServiceHTTPServer(h.srv, e.mini)
	return h
}

func (h *miniappHTTP) post(path, body string) (int, map[string]any, http.Header) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:4321"
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

// 一键登录和静默续期的线上形态，以及登录审计：成功时记已验证的用户 ID 和用户名；
// 失败且没匹配到用户时用户名留空，失败原因只记 reason。审计记录里没有手机号和 code。
// 静默续期不进登录审计，和管理端刷新一样。
func TestMiniappHTTP_ContractAndLoginAudit(t *testing.T) {
	e := newMiniappEnv(t)
	uid := e.seedUser("zhangsan", "13800000001")
	h := newMiniappHTTP(e)

	// 成功：扁平请求体，字段名 code / phoneCode。
	code, phoneCode := e.wxCode("openid-http-0001"), e.phoneCode("13800000001")
	status, body, header := h.post("/admin/v1/miniapp/login/phone", fmt.Sprintf(`{"code":%q,"phoneCode":%q}`, code, phoneCode))
	require.Equal(t, 200, status, "body=%v", body)
	require.Equal(t, "bearer", body["token_type"])
	require.NotEmpty(t, body["access_token"])
	require.Equal(t, true, body["miniapp_bound"])
	// expires_in 是 int64，线上是字符串。
	require.Equal(t, fmt.Sprintf("%d", int64(e.authenticator.GetAccessTokenExpires(1).Seconds())), body["expires_in"])
	require.Empty(t, header.Values("Set-Cookie"), "小程序登录不下发 Cookie")
	require.True(t, e.tokenValid(body["access_token"].(string)))

	// 失败：手机号没开通。
	status, body, _ = h.post("/admin/v1/miniapp/login/phone",
		fmt.Sprintf(`{"code":%q,"phoneCode":%q}`, e.wxCode("openid-http-0002"), e.phoneCode("13800009999")))
	require.Equal(t, 403, status)
	require.Equal(t, "MINIAPP_PHONE_NOT_REGISTERED", body["reason"])
	require.EqualValues(t, 403, body["code"])

	// 请求体不合规（缺 phoneCode）在入参校验处就拒绝。多包一层 data 也一样：字段全为空。
	wxCalls := e.wx.Calls()
	status, _, _ = h.post("/admin/v1/miniapp/login/phone", `{"code":"abc"}`)
	require.Equal(t, 400, status)
	status, _, _ = h.post("/admin/v1/miniapp/login/phone", `{"data":{"code":"abc","phoneCode":"def"}}`)
	require.Equal(t, 400, status)
	require.Equal(t, wxCalls, e.wx.Calls())

	// 静默续期。
	silentCode := e.wxCode("openid-http-0001")
	status, body, _ = h.post("/admin/v1/miniapp/login/silent", fmt.Sprintf(`{"code":%q}`, silentCode))
	require.Equal(t, 200, status, "body=%v", body)
	require.Equal(t, true, body["miniapp_bound"])
	status, body, _ = h.post("/admin/v1/miniapp/login/silent", fmt.Sprintf(`{"code":%q}`, e.wxCode("openid-http-stranger")))
	require.Equal(t, 403, status)
	require.Equal(t, "MINIAPP_NOT_BOUND", body["reason"])

	// 登录审计：一键登录两条（成功、失败）加入参不合规的两条；静默续期没有。操作审计里这些接口都不记。
	require.Len(t, h.logins, 4)
	require.Empty(t, h.ops)

	ok := h.logins[0]
	require.Equal(t, auditV1.LoginAuditLog_LOGIN, ok.GetActionType())
	require.Equal(t, auditV1.LoginAuditLog_SUCCESS, ok.GetStatus())
	require.Equal(t, uid, ok.GetUserId(), "成功时用已验证的用户 ID")
	require.Equal(t, "zhangsan", ok.GetUsername())
	require.Equal(t, "10.0.0.1", ok.GetIpAddress())

	failed := h.logins[1]
	require.Equal(t, auditV1.LoginAuditLog_FAILED, failed.GetStatus())
	require.Zero(t, failed.GetUserId())
	require.Empty(t, failed.GetUsername(), "没匹配到用户时用户名留空")
	require.Equal(t, "MINIAPP_PHONE_NOT_REGISTERED", failed.GetFailureReason())

	for i, rec := range h.logins {
		raw, err := protojson.Marshal(rec)
		require.NoError(t, err)
		for _, secret := range []string{"13800000001", "13800009999", "SECRETCODE", code, phoneCode, "openid-http-0001"} {
			require.NotContains(t, string(raw), secret, "第 %d 条登录审计", i)
		}
	}
	e.requireLogsClean("13800000001", "13800009999")
}
