package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	ktransport "github.com/go-kratos/kratos/v2/transport"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	entCrud "go-wind-admin/third_party/go-crud/entgo"
	"go-wind-admin/third_party/go-utils/captcha"
	"go-wind-admin/third_party/go-utils/password"
	conf "go-wind-admin/third_party/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
	"go-wind-admin/app/admin/service/internal/data/enttest"

	"go-wind-admin/pkg/constants"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/wechat"
	"go-wind-admin/pkg/wechat/wechattest"
)

// ---- 小程序登录测试的公共装配 ----
//
// 全部用真实实现：SQLite 内存库上的仓储，miniredis 上的令牌缓存、请求额度和验证码，
// 真实的微信客户端指向 wechattest 假服务端（按接口计数）。能注入的故障：
//   - Redis：cmdFault 按命令的任意参数匹配，让命中的命令失败；
//   - 微信：假服务端的 SetError / SetHang，或 wxHook 在调用微信的那一刻插入一段操作；
//   - 数据库：ent 钩子让写绑定行失败（failBindingInsert）。

// cmdFault 让匹配的 Redis 命令失败。和 redisFault 的区别：按全部参数匹配，
// 脚本命令（EVALSHA）的键不在第一个参数上。
type cmdFault struct {
	mu    sync.Mutex
	match func(name string, args []string) bool
}

func (h *cmdFault) set(match func(name string, args []string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.match = match
}

// failKeys 让参数里带有 substr 的命令失败（substr 为空表示恢复正常）。
func (h *cmdFault) failKeys(substr string) {
	if substr == "" {
		h.set(nil)
		return
	}
	h.set(func(_ string, args []string) bool {
		for _, a := range args {
			if strings.Contains(a, substr) {
				return true
			}
		}
		return false
	})
}

func (h *cmdFault) DialHook(next redis.DialHook) redis.DialHook {
	return func(c context.Context, n, a string) (net.Conn, error) { return next(c, n, a) }
}
func (h *cmdFault) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *cmdFault) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.mu.Lock()
		match := h.match
		h.mu.Unlock()
		if match != nil {
			args := make([]string, 0, len(cmd.Args()))
			for _, a := range cmd.Args() {
				args = append(args, fmt.Sprint(a))
			}
			if match(strings.ToLower(cmd.Name()), args) {
				cmd.SetErr(errInjectedRedisFault)
				return errInjectedRedisFault
			}
		}
		return next(ctx, cmd)
	}
}

// wxHook 包一层微信客户端：在调用微信之前执行 beforeCode2Session（只执行一次），
// 用来在「读完撤销版本、通过认证」和「签发」之间插入一段操作。
type wxHook struct {
	wechat.MiniappClient
	mu                 sync.Mutex
	beforeCode2Session func()
}

func (h *wxHook) Code2Session(ctx context.Context, code string) (string, error) {
	h.mu.Lock()
	fn := h.beforeCode2Session
	h.beforeCode2Session = nil
	h.mu.Unlock()
	if fn != nil {
		fn()
	}
	return h.MiniappClient.Code2Session(ctx, code)
}

const miniappQuotaKeys = "gowind:miniapp:quota:"

type miniappEnv struct {
	t         *testing.T
	sys       context.Context
	entClient *entCrud.EntClient[*ent.Client]
	client    *ent.Client
	mr        *miniredis.Miniredis
	fault     *cmdFault
	logs      *bytes.Buffer
	wx        *wechattest.Server
	hook      *wxHook
	captcha   *captcha.Captcha

	authenticator *data.Authenticator
	userRepo      data.UserRepo
	credRepo      *data.UserCredentialRepo

	authn   *AuthenticationService
	mini    *MiniappAuthService
	users   *UserService
	profile *UserProfileService
	online  *OnlineSessionService
	revoker *BindingRevoker
	sender  *fakeContactSender // 短信出口的替身：记下发出去的验证码

	// revoked 全量吊销钩子收到的客户端类型，按发生顺序。
	revokedMu sync.Mutex
	revoked   []authenticationV1.ClientType

	seed *authSvcEnv // 复用 seedBackendAccessPermission / seedRole
	// roleID 带「访问后台」权限点的普通角色；seedUser 默认挂它。
	roleID uint32
	permID uint32

	failBindingInsert atomic.Bool
	seq               atomic.Int64
}

func newMiniappEnv(t *testing.T) *miniappEnv {
	t.Helper()
	entClient := enttest.NewEntClientForTest(t)
	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())

	mr := miniredis.RunT(t)
	fault := &cmdFault{}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	rdb.AddHook(fault)
	t.Cleanup(func() { _ = rdb.Close() })

	authenticator, err := data.NewAuthenticatorForTest(&conf.Authentication_Jwt{Method: "HS256", Key: authSvcTestJWTKey}, data.NewUserTokenCacheForTest(rdb))
	require.NoError(t, err)

	// 服务日志写进缓冲区，用来断言里面没有 code、Secret、完整手机号。
	logs := &bytes.Buffer{}
	logger := bLogger.NewHelper(bLogger.NewSlogLogger(slog.New(slog.NewTextHandler(&lockedWriter{w: logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	userRepo := data.NewUserRepo(boot, entClient,
		data.NewUserRoleRepo(boot, entClient), data.NewUserOrgUnitRepo(boot, entClient), data.NewUserPositionRepo(boot, entClient), nil)
	credRepo := data.NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), data.NewConfigRepoForTest(entClient, rdb))
	roleRepo := data.NewRoleRepoForTest(entClient)

	captchaClient := captcha.NewCaptcha(rdb,
		captcha.WithDriverType(captcha.DriverMath),
		captcha.WithExpire(10*time.Minute),
		captcha.WithKeyPrefix(authSvcTestKeyPrefix),
	)

	wx := wechattest.New(t)
	hook := &wxHook{MiniappClient: wechat.NewMiniappClient(wechat.Config{
		AppID: wechattest.AppID, AppSecret: wechattest.AppSecret, BaseURL: wx.URL,
		HTTPClient: &http.Client{Timeout: 300 * time.Millisecond}, // 超时用例不等 5 秒
	}, rdb)}
	runtime := &MiniappRuntime{AppID: wechattest.AppID, Client: hook, Quota: data.NewMiniappQuotaForTest(rdb)}

	authn := &AuthenticationService{
		log:                     logger,
		userRepo:                userRepo,
		userCredentialRepo:      credRepo,
		roleRepo:                roleRepo,
		orgUnitRepo:             data.NewOrgUnitRepoForTest(entClient),
		roleOrgUnitRepo:         data.NewRoleOrgUnitRepoForTest(entClient),
		roleFieldPermissionRepo: data.NewRoleFieldPermissionRepoForTest(entClient),
		permissionRepo:          data.NewPermissionRepoForTest(entClient),
		authenticator:           authenticator,
		clientType:              authenticationV1.ClientType_admin,
		captchaClient:           captchaClient,
		rateLimiter:             data.NewLoginRateLimiterForTest(rdb),
		miniapp:                 runtime,
	}
	sys := enttest.NewSystemContext(context.Background())
	sender := &fakeContactSender{smsConfigured: true}

	e := &miniappEnv{
		t: t, sys: sys, entClient: entClient, client: entClient.Client(), mr: mr, fault: fault, logs: logs,
		wx: wx, hook: hook, captcha: captchaClient,
		authenticator: authenticator, userRepo: userRepo, credRepo: credRepo,
		authn: authn,
		users: &UserService{
			log: logger, userRepo: userRepo, userCredentialRepo: credRepo, roleRepo: roleRepo,
			positionRepo: data.NewPositionRepoForTest(entClient), orgUnitRepo: data.NewOrgUnitRepoForTest(entClient),
			authenticator: authenticator,
		},
		profile: &UserProfileService{
			log: logger, userRepo: userRepo, roleRepo: roleRepo, userCredentialRepo: credRepo,
			authenticator: authenticator, outbound: sender,
			vcodeCache: data.NewVCodeCacheForTest(rdb),
		},
		sender: sender,
		seed:   &authSvcEnv{svc: authn, entClient: entClient, mr: mr, captchaClient: captchaClient, ctx: sys},
	}
	e.revoker = &BindingRevoker{log: logger, userCredentialRepo: credRepo, authenticator: authenticator}
	e.online = &OnlineSessionService{log: logger, authenticator: authenticator, revoker: e.revoker}
	e.mini = newMiniappAuthServiceForTest(e, logger, runtime)
	// 记下每次全量吊销的客户端类型：只吊销 app 令牌时管理端的 SSE 连接不该被断开（钩子按类型断流）。
	authenticator.OnUserTokensRevoked(func(_ context.Context, ct authenticationV1.ClientType, _ uint32) {
		e.revokedMu.Lock()
		defer e.revokedMu.Unlock()
		e.revoked = append(e.revoked, ct)
	})

	// 数据库故障注入：打开开关后，写微信绑定行失败。事务客户端和普通客户端共用这组钩子。
	e.client.UserCredential.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if e.failBindingInsert.Load() && m.Op().Is(ent.OpCreate) {
				if cm, ok := m.(*ent.UserCredentialMutation); ok {
					if p, exists := cm.Provider(); exists && p == data.MiniappProvider {
						return nil, fmt.Errorf("injected database failure")
					}
				}
			}
			return next.Mutate(ctx, m)
		})
	})

	e.permID = e.seed.seedBackendAccessPermission(t)
	e.roleID = e.seed.seedRole(t, nil, permissionV1.Role_SYSTEM, "miniapp-user", []uint32{e.permID}, nil, nil)
	return e
}

// lockedWriter 并发用例里多个 goroutine 同时写日志。
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// reqCtx 带客户端 IP 的 HTTP 请求上下文（额度按 IP 计）。
func (e *miniappEnv) reqCtx(ip string) context.Context {
	req := &http.Request{Header: http.Header{}, RemoteAddr: ip + ":4321"}
	return ktransport.NewServerContext(context.Background(), &fakeHeaderTransporter{req: req})
}

// captchaCtx 在 reqCtx 的基础上带一对新的图形验证码（密码登录要）。
func (e *miniappEnv) captchaCtx(ip string) context.Context {
	e.t.Helper()
	id, _, answer, err := e.captcha.Generate()
	require.NoError(e.t, err)
	require.NoError(e.t, e.captcha.Save(context.Background(), id, answer))
	req := &http.Request{Header: http.Header{}, RemoteAddr: ip + ":4321"}
	req.Header.Set("X-Captcha-Id", id)
	req.Header.Set("X-Captcha-Value", answer)
	return ktransport.NewServerContext(context.Background(), &fakeHeaderTransporter{req: req})
}

// seedUser 建一个启用的用户：带手机号、用户名密码凭证（密码是 constants.DefaultUserPassword）和角色。
// 不传角色时挂默认的「访问后台」角色。
func (e *miniappEnv) seedUser(username, mobile string, roleIDs ...uint32) uint32 {
	e.t.Helper()
	if len(roleIDs) == 0 {
		roleIDs = []uint32{e.roleID}
	}
	id := e.client.User.Create().SetUsername(username).SetMobile(mobile).SetNickname(username).SaveX(e.sys).ID
	for _, rid := range roleIDs {
		if rid != 0 {
			e.client.UserRole.Create().SetUserID(id).SetRoleID(rid).SaveX(e.sys)
		}
	}
	e.seed.seedCredential(e.t, nil, id, username, authenticationV1.UserCredential_ENABLED)
	return id
}

// wxCode 登记一个新的 wx.login code，换出来的是 openid。每个 code 只能用一次。
func (e *miniappEnv) wxCode(openid string) string {
	code := fmt.Sprintf("wxcode-%d-SECRETCODE", e.seq.Add(1))
	e.wx.AddCode(code, openid)
	return code
}

// phoneCode 登记一个新的手机号 code，换出来的是区号 86 和 mobile。
func (e *miniappEnv) phoneCode(mobile string) string {
	code := fmt.Sprintf("phonecode-%d-SECRETCODE", e.seq.Add(1))
	e.wx.AddPhone(code, wechattest.PhoneReply{CountryCode: "86", PurePhoneNumber: mobile})
	return code
}

func (e *miniappEnv) phoneLogin(ip, openid, mobile string) (*authenticationV1.LoginResponse, error) {
	return e.mini.LoginByPhone(e.reqCtx(ip), &authenticationV1.MiniappPhoneLoginRequest{
		Code: e.wxCode(openid), PhoneCode: e.phoneCode(mobile),
	})
}

func (e *miniappEnv) silentLogin(ip, openid string) (*authenticationV1.LoginResponse, error) {
	return e.mini.SilentLogin(e.reqCtx(ip), &authenticationV1.MiniappSilentLoginRequest{Code: e.wxCode(openid)})
}

// passwordLogin 小程序账号密码登录。openid 为空表示不带 code（H5）。
func (e *miniappEnv) passwordLogin(ip, username, plainPassword, openid string) (*authenticationV1.LoginResponse, error) {
	e.t.Helper()
	req := loginReq(username, encryptLoginPassword(e.t, plainPassword))
	req.ClientType = authenticationV1.ClientType_app.Enum()
	if openid != "" {
		code := e.wxCode(openid)
		req.Code = &code
	}
	return e.authn.Login(e.captchaCtx(ip), req)
}

// adminPasswordLogin 管理端账号密码登录。
func (e *miniappEnv) adminPasswordLogin(ip, username, plainPassword string) (*authenticationV1.LoginResponse, error) {
	e.t.Helper()
	return e.authn.Login(e.captchaCtx(ip), loginReq(username, encryptLoginPassword(e.t, plainPassword)))
}

// bindings 用户的微信绑定行。
func (e *miniappEnv) bindings(userID uint32) []*ent.UserCredential {
	return e.client.UserCredential.Query().
		Where(usercredential.UserIDEQ(userID), usercredential.ProviderEQ(data.MiniappProvider)).
		AllX(e.sys)
}

// payload 校验令牌并返回它的载荷；令牌无效时返回 nil。
func (e *miniappEnv) payload(token string) *authenticationV1.UserTokenPayload {
	resp, err := e.authenticator.Authenticate(context.Background(), &authenticationV1.ValidateTokenRequest{
		Token: token, TokenCategory: authenticationV1.TokenCategory_ACCESS,
	})
	if err != nil || !resp.GetIsValid() {
		return nil
	}
	return resp.GetPayload()
}

func (e *miniappEnv) tokenValid(token string) bool { return e.payload(token) != nil }

// appTokens 用户在 Redis 里的全部 app access token。
func (e *miniappEnv) appTokens(userID uint32) []string {
	return e.authenticator.GetAccessTokens(context.Background(), authenticationV1.ClientType_app, userID)
}

// requireLogsClean 服务日志里不能有微信 code、Secret、access_token 和给定的敏感值（完整手机号、openid 等）。
func (e *miniappEnv) requireLogsClean(secrets ...string) {
	e.t.Helper()
	text := e.logs.String()
	for _, secret := range append(secrets, "SECRETCODE", wechattest.AppSecret, wechattest.AccessToken, e.wx.URL) {
		require.NotContains(e.t, text, secret, "服务日志里不应出现 %q", secret)
	}
}

// revokedTypes 取走到目前为止全量吊销钩子收到的客户端类型。
func (e *miniappEnv) revokedTypes() []authenticationV1.ClientType {
	e.revokedMu.Lock()
	defer e.revokedMu.Unlock()
	out := e.revoked
	e.revoked = nil
	return out
}

// adminToken 给用户签一个管理端会话，返回 access token。
func (e *miniappEnv) adminToken(userID uint32) string {
	e.t.Helper()
	at, _, err := e.authenticator.CreateUserToken(context.Background(), authenticationV1.ClientType_admin,
		&authenticationV1.UserTokenPayload{UserId: userID})
	require.NoError(e.t, err)
	return at
}

// operatorOf 用令牌自己的载荷组出请求上下文（相当于过了鉴权中间件），底下是系统视图。
func (e *miniappEnv) operatorOf(token string) context.Context {
	e.t.Helper()
	payload := e.payload(token)
	require.NotNil(e.t, payload, "令牌应该有效")
	return auth.NewContext(e.sys, payload)
}

// defaultPassword 种子用户的明文密码。
const defaultPassword = constants.DefaultUserPassword
