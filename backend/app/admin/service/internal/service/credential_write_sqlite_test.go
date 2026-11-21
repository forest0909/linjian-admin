package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"go-wind-admin/third_party/go-utils/password"
	"go-wind-admin/third_party/go-utils/trans"
	conf "go-wind-admin/third_party/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
	"go-wind-admin/app/admin/service/internal/data/enttest"

	"go-wind-admin/pkg/constants"
	"go-wind-admin/pkg/middleware/auth"
)

// ---- 公共装配：真实 userRepo + 凭证仓储 + miniredis 上的令牌缓存和验证码缓存 ----

// redisFault 让匹配的 Redis 命令失败（按命令名和首个键）。match 为 nil 时不拦。
type redisFault struct {
	mu    sync.Mutex
	match func(name, key string) bool
}

var errInjectedRedisFault = errors.New("injected redis failure")

func (h *redisFault) set(match func(name, key string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.match = match
}
func (h *redisFault) DialHook(next redis.DialHook) redis.DialHook {
	return func(c context.Context, n, a string) (net.Conn, error) { return next(c, n, a) }
}
func (h *redisFault) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *redisFault) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.mu.Lock()
		match := h.match
		h.mu.Unlock()
		key := ""
		if args := cmd.Args(); len(args) > 1 {
			key = fmt.Sprint(args[1])
		}
		if match != nil && match(strings.ToLower(cmd.Name()), key) {
			cmd.SetErr(errInjectedRedisFault)
			return errInjectedRedisFault
		}
		return next(ctx, cmd)
	}
}

// fakeContactSender 记录发出去的验证码，不拨号。
type fakeContactSender struct {
	smsConfigured bool
	sms           map[string]string // 号码 → 最近一次的验证码
	smsCount      int
	mailBody      map[string]string // 收件地址 → 最近一次的正文
}

func (f *fakeContactSender) SMSConfigured() bool { return f.smsConfigured }
func (f *fakeContactSender) SendSMS(_ context.Context, phone, code string) error {
	if !f.smsConfigured {
		return ErrOutboundNotConfigured
	}
	if f.sms == nil {
		f.sms = map[string]string{}
	}
	f.sms[phone] = code
	f.smsCount++
	return nil
}
func (f *fakeContactSender) SendMail(_ context.Context, to, _, body string) error {
	if f.mailBody == nil {
		f.mailBody = map[string]string{}
	}
	f.mailBody[to] = body
	return nil
}

type credWriteEnv struct {
	sys       context.Context
	client    *ent.Client
	users     *UserService
	profile   *UserProfileService
	sender    *fakeContactSender
	authn     *data.Authenticator
	fault     *redisFault
	logs      *bytes.Buffer
	userSvcEn *userServiceEnv // 复用 seedRoleWithPerms / seedUserSvcCredential / findCred
}

func newCredWriteEnv(t *testing.T) *credWriteEnv {
	t.Helper()
	entClient := enttest.NewEntClientForTest(t)
	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())

	mr := miniredis.RunT(t)
	fault := &redisFault{}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	rdb.AddHook(fault)
	t.Cleanup(func() { _ = rdb.Close() })

	authn, err := data.NewAuthenticatorForTest(&conf.Authentication_Jwt{Method: "HS256", Key: authSvcTestJWTKey}, data.NewUserTokenCacheForTest(rdb))
	require.NoError(t, err)

	// 服务日志写进缓冲区，用来断言里面没有完整手机号和验证码。
	logs := &bytes.Buffer{}
	logger := bLogger.NewHelper(bLogger.NewSlogLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	userRepo := data.NewUserRepo(boot, entClient,
		data.NewUserRoleRepo(boot, entClient), data.NewUserOrgUnitRepo(boot, entClient), data.NewUserPositionRepo(boot, entClient), nil)
	credRepo := data.NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), data.NewConfigRepoForTest(entClient, rdb))
	roleRepo := data.NewRoleRepoForTest(entClient)

	users := &UserService{
		log:                logger,
		userRepo:           userRepo,
		userCredentialRepo: credRepo,
		roleRepo:           roleRepo,
		positionRepo:       data.NewPositionRepoForTest(entClient),
		orgUnitRepo:        data.NewOrgUnitRepoForTest(entClient),
		authenticator:      authn,
	}
	sender := &fakeContactSender{smsConfigured: true}
	profile := &UserProfileService{
		log:                logger,
		userRepo:           userRepo,
		roleRepo:           roleRepo,
		userCredentialRepo: credRepo,
		authenticator:      authn,
		outbound:           sender,
		vcodeCache:         data.NewVCodeCacheForTest(rdb),
	}
	sys := enttest.NewSystemContext(context.Background())
	return &credWriteEnv{
		sys: sys, client: entClient.Client(), users: users, profile: profile, sender: sender,
		authn: authn, fault: fault, logs: logs,
		userSvcEn: &userServiceEnv{svc: users, ctx: sys},
	}
}

// seedUser 建一个带手机号、用户名凭证和角色的用户。
func (e *credWriteEnv) seedUser(t *testing.T, username, mobile string, roleIDs ...uint32) uint32 {
	t.Helper()
	id := e.client.User.Create().SetUsername(username).SetMobile(mobile).SetNickname("old-nick").SaveX(e.sys).ID
	for _, rid := range roleIDs {
		e.client.UserRole.Create().SetUserID(id).SetRoleID(rid).SaveX(e.sys)
	}
	e.userSvcEn.seedUserSvcCredential(t, nil, id, username)
	return id
}

func (e *credWriteEnv) mobile(t *testing.T, id uint32) string {
	t.Helper()
	u := e.client.User.GetX(e.sys, id)
	if u.Mobile == nil {
		return ""
	}
	return *u.Mobile
}

func (e *credWriteEnv) nickname(t *testing.T, id uint32) string {
	t.Helper()
	u := e.client.User.GetX(e.sys, id)
	if u.Nickname == nil {
		return ""
	}
	return *u.Nickname
}

func (e *credWriteEnv) issue(t *testing.T, ct authenticationV1.ClientType, userID uint32) string {
	t.Helper()
	at, _, err := e.authn.CreateUserToken(context.Background(), ct, &authenticationV1.UserTokenPayload{UserId: userID})
	require.NoError(t, err)
	return at
}

func (e *credWriteEnv) tokenValid(token string) bool {
	resp, err := e.authn.Authenticate(context.Background(), &authenticationV1.ValidateTokenRequest{
		Token: token, TokenCategory: authenticationV1.TokenCategory_ACCESS,
	})
	return err == nil && resp.GetIsValid()
}

// selfCtx 普通用户自己的请求上下文。
func (e *credWriteEnv) selfCtx(userID uint32) context.Context {
	return auth.NewContext(e.sys, &authenticationV1.UserTokenPayload{UserId: userID})
}

func bindPhoneReq(phone string) *identityV1.BindContactRequest {
	return &identityV1.BindContactRequest{Contact: &identityV1.BindContactRequest_Phone{Phone: &identityV1.BindPhoneRequest{Phone: phone}}}
}

func verifyPhoneReq(phone, code string) *identityV1.VerifyContactRequest {
	return &identityV1.VerifyContactRequest{Contact: &identityV1.VerifyContactRequest_Phone{Phone: &identityV1.PhoneVerification{Phone: phone, Code: code}}}
}

// ---- 自助改号 ----

// PUT /me 带 mobile 不生效：手机号是登录凭证，自助改号只能走短信验证码。
func TestProfileUpdate_MobileIsNotSelfEditable(t *testing.T) {
	e := newCredWriteEnv(t)
	id := e.seedUser(t, "self", "13800000001")
	ctx := e.selfCtx(id)

	for name, mask := range map[string]*fieldmaskpb.FieldMask{
		"mask 里带 mobile": {Paths: []string{"mobile", "nickname"}},
		"没有 mask":        nil,
		"mask 只有 mobile": {Paths: []string{"mobile"}},
	} {
		_, err := e.profile.UpdateUser(ctx, &identityV1.UpdateUserRequest{
			Data:       &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("nick-" + name)},
			UpdateMask: mask,
		})
		require.NoError(t, err, name)
		require.Equal(t, "13800000001", e.mobile(t, id), name)
	}
	// 其他可编辑字段照常生效（最后一次带 nickname 的请求是前两种之一）。
	require.True(t, strings.HasPrefix(e.nickname(t, id), "nick-"))

	// mask 里一个可编辑字段都没有：什么都不写，不会退化成全量更新。
	require.NoError(t, e.client.User.UpdateOneID(id).SetNickname("keep").Exec(e.sys))
	_, err := e.profile.UpdateUser(ctx, &identityV1.UpdateUserRequest{
		Data:       &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("overwritten"), RoleIds: []uint32{1}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile", "role_ids"}},
	})
	require.NoError(t, err)
	require.Equal(t, "keep", e.nickname(t, id))
	require.Equal(t, "13800000001", e.mobile(t, id))
}

func TestBindContact_Phone(t *testing.T) {
	t.Run("短信没配置返回 NOT_IMPLEMENTED，不占额度", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		e.sender.smsConfigured = false
		for i := 0; i < 3; i++ {
			_, err := e.profile.BindContact(e.selfCtx(id), bindPhoneReq("13900000002"))
			require.True(t, authenticationV1.IsNotImplemented(err), "got %v", err)
		}
	})

	t.Run("号码不是中国大陆 11 位被拒", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		for _, bad := range []string{"", "12345", "+85212345678"} {
			_, err := e.profile.BindContact(e.selfCtx(id), bindPhoneReq(bad))
			require.True(t, authenticationV1.IsBadRequest(err), "%q: %v", bad, err)
		}
		require.Zero(t, e.sender.smsCount)
	})

	t.Run("发送超过额度被拒", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		_, err := e.profile.BindContact(e.selfCtx(id), bindPhoneReq("13900000002"))
		require.NoError(t, err)
		// 同一号码一分钟内第二次；带 +86 的写法是同一个号码。
		_, err = e.profile.BindContact(e.selfCtx(id), bindPhoneReq("+8613900000002"))
		require.True(t, authenticationV1.IsTooManyRequests(err), "got %v", err)
		require.Equal(t, 1, e.sender.smsCount)
	})

	t.Run("Redis 出错时拒绝发送", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		e.fault.set(func(string, string) bool { return true })
		_, err := e.profile.BindContact(e.selfCtx(id), bindPhoneReq("13900000002"))
		require.True(t, authenticationV1.IsServiceUnavailable(err), "got %v", err)
		require.Zero(t, e.sender.smsCount)
	})

	t.Run("没登录", func(t *testing.T) {
		e := newCredWriteEnv(t)
		_, err := e.profile.BindContact(context.Background(), bindPhoneReq("13900000002"))
		require.Error(t, err)
	})
}

func TestVerifyContact_Phone(t *testing.T) {
	const oldMobile, newMobile = "13800000001", "13900000002"

	// requestCode 走一遍 BindContact，返回发出去的验证码。
	requestCode := func(t *testing.T, e *credWriteEnv, userID uint32, phone string) string {
		t.Helper()
		_, err := e.profile.BindContact(e.selfCtx(userID), bindPhoneReq(phone))
		require.NoError(t, err)
		code := e.sender.sms[phone]
		require.Len(t, code, 6)
		return code
	}

	t.Run("成功：更新 mobile、只留一行 PHONE 凭证、app 会话失效", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		// 旧流程留下的 PHONE 凭证行。
		e.client.UserCredential.Create().SetUserID(id).SetIdentityType(usercredential.IdentityTypePhone).
			SetIdentifier(oldMobile).SetCredential("x").SaveX(e.sys)
		appAT := e.issue(t, authenticationV1.ClientType_app, id)
		adminAT := e.issue(t, authenticationV1.ClientType_admin, id)

		code := requestCode(t, e, id, newMobile)
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
		require.NoError(t, err)

		require.Equal(t, newMobile, e.mobile(t, id))
		rows := e.client.UserCredential.Query().
			Where(usercredential.UserIDEQ(id), usercredential.IdentityTypeEQ(usercredential.IdentityTypePhone)).AllX(e.sys)
		require.Len(t, rows, 1)
		require.Equal(t, newMobile, *rows[0].Identifier)

		require.False(t, e.tokenValid(appAT), "改号后 app 会话应失效")
		require.True(t, e.tokenValid(adminAT), "管理端会话不受影响")

		// 同一个验证码不能再用。
		_, err = e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
		require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)

		// 整个流程的日志里没有完整手机号和验证码。
		logs := e.logs.String()
		require.NotContains(t, logs, newMobile)
		require.NotContains(t, logs, oldMobile)
		require.NotContains(t, logs, code)
		require.Contains(t, logs, "139****0002")
	})

	t.Run("别的用户申请的验证码不能用", func(t *testing.T) {
		e := newCredWriteEnv(t)
		owner := e.seedUser(t, "owner", oldMobile)
		thief := e.seedUser(t, "thief", "13700000007")
		code := requestCode(t, e, owner, newMobile)

		_, err := e.profile.VerifyContact(e.selfCtx(thief), verifyPhoneReq(newMobile, code))
		require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)
		require.Equal(t, "13700000007", e.mobile(t, thief))

		// 申请人自己照常能用。
		_, err = e.profile.VerifyContact(e.selfCtx(owner), verifyPhoneReq(newMobile, code))
		require.NoError(t, err)
		require.Equal(t, newMobile, e.mobile(t, owner))
	})

	t.Run("错 5 次后正确的也不行", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		code := requestCode(t, e, id, newMobile)
		wrong := "000000"
		if code == wrong {
			wrong = "111111"
		}
		for i := 0; i < data.VCodeMaxAttempts; i++ {
			_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, wrong))
			require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)
		}
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
		require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)
		require.Equal(t, oldMobile, e.mobile(t, id))
	})

	t.Run("并发提交同一个正确验证码只有一个成功", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		code := requestCode(t, e, id, newMobile)
		var mu sync.Mutex
		var okCount, rejected int
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					okCount++
				} else if authenticationV1.IsBadRequest(err) {
					rejected++
				}
			}()
		}
		wg.Wait()
		require.Equal(t, 1, okCount)
		require.Equal(t, 7, rejected)
		require.Equal(t, newMobile, e.mobile(t, id))
	})

	t.Run("Redis 出错时拒绝改号", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		code := requestCode(t, e, id, newMobile)
		e.fault.set(func(name, _ string) bool { return name == "evalsha" || name == "eval" })
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
		require.True(t, authenticationV1.IsServiceUnavailable(err), "got %v", err)
		require.Equal(t, oldMobile, e.mobile(t, id))
	})

	t.Run("号码被别人使用返回 CONFLICT", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		e.seedUser(t, "other", newMobile)
		appAT := e.issue(t, authenticationV1.ClientType_app, id)
		code := requestCode(t, e, id, newMobile)
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
		require.True(t, authenticationV1.IsConflict(err), "got %v", err)
		require.Equal(t, oldMobile, e.mobile(t, id))
		require.True(t, e.tokenValid(appAT), "没改成，会话不动")
		require.NotContains(t, err.Error(), newMobile, "返回给前端的错误不带联系方式")
	})

	t.Run("新旧号码相同：什么都不做，会话也不动", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		appAT := e.issue(t, authenticationV1.ClientType_app, id)
		before := e.client.User.GetX(e.sys, id).UpdatedAt
		code := requestCode(t, e, id, oldMobile)
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(oldMobile, code))
		require.NoError(t, err)
		require.Equal(t, oldMobile, e.mobile(t, id))
		require.Equal(t, before, e.client.User.GetX(e.sys, id).UpdatedAt)
		require.Zero(t, e.client.UserCredential.Query().
			Where(usercredential.UserIDEQ(id), usercredential.IdentityTypeEQ(usercredential.IdentityTypePhone)).CountX(e.sys))
		require.True(t, e.tokenValid(appAT))
	})

	t.Run("吊销失败返回 503，数据库已提交", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", oldMobile)
		appAT := e.issue(t, authenticationV1.ClientType_app, id)

		code := requestCode(t, e, id, newMobile)
		e.fault.set(func(name, key string) bool { return name == "del" && strings.HasPrefix(key, "at:") })
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, code))
		require.True(t, authenticationV1.IsServiceUnavailable(err), "got %v", err)
		require.Equal(t, newMobile, e.mobile(t, id), "数据库已提交")
		// 接受的残留：没删掉的令牌在自己的有效期内仍能用。
		require.True(t, e.tokenValid(appAT))
	})
}

func bindEmailReq(email string) *identityV1.BindContactRequest {
	return &identityV1.BindContactRequest{Contact: &identityV1.BindContactRequest_Email{Email: &identityV1.BindEmailRequest{Email: email}}}
}

// 短信和邮件两个渠道的验证码、号码额度互不相通。
func TestContactVCode_ChannelsAreIsolated(t *testing.T) {
	const newMobile = "13900000002"

	t.Run("邮件渠道签发的验证码不能过短信验证", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		// 直接以邮件渠道为同一段文本存一枚验证码（模拟 SMTP 接受了本地收件人地址）。
		require.NoError(t, e.profile.vcodeCache.SaveForUser(context.Background(), id, bindContactPurpose, contactKindEmail, newMobile, "246810", bindContactCodeTTL))
		_, err := e.profile.VerifyContact(e.selfCtx(id), verifyPhoneReq(newMobile, "246810"))
		require.True(t, authenticationV1.IsBadRequest(err), "got %v", err)
		require.Equal(t, "13800000001", e.mobile(t, id))
	})

	t.Run("格式不对的邮箱在扣额度和存验证码之前被拒", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		for _, bad := range []string{newMobile, "not-an-email", "a@b@c", "Alice <a@example.com>"} {
			_, err := e.profile.BindContact(e.selfCtx(id), bindEmailReq(bad))
			require.True(t, authenticationV1.IsBadRequest(err), "%q: %v", bad, err)
		}
		// 上面的邮件请求没有占用号码额度，也没有占用户额度：短信照常能发。
		_, err := e.profile.BindContact(e.selfCtx(id), bindPhoneReq(newMobile))
		require.NoError(t, err)
		require.Equal(t, 1, e.sender.smsCount)
	})

	t.Run("邮箱发码和验证用同一套规范", func(t *testing.T) {
		e := newCredWriteEnv(t)
		id := e.seedUser(t, "self", "13800000001")
		_, err := e.profile.BindContact(e.selfCtx(id), bindEmailReq(" Alice@Example.com "))
		require.NoError(t, err)
		require.NotEmpty(t, e.sender.mailBody["alice@example.com"])
	})
}

// 发送失败的错误文本里没有收件地址和验证码。
func TestScrubSendError(t *testing.T) {
	cause := errors.New("smtp RCPT failed for Alice@Example.com: 550 5.1.1 <alice@example.com>: Recipient address rejected")
	err := scrubSendError(fmt.Errorf("send mail failed: %w", cause), "alice@example.com")
	require.NotContains(t, strings.ToLower(err.Error()), "alice@example.com")
	require.Contains(t, err.Error(), "550 5.1.1")
	require.ErrorIs(t, err, cause)

	err = scrubSendError(fmt.Errorf("%w: peer answered 400: {\"PhoneNumbers\":\"+8613800000001\",\"code\":\"482913\"}", ErrOutboundNotConfigured), "13800000001", "482913")
	require.NotContains(t, err.Error(), "13800000001")
	require.NotContains(t, err.Error(), "482913")
	require.ErrorIs(t, err, ErrOutboundNotConfigured)

	require.NoError(t, scrubSendError(nil, "x"))
}

// ---- 管理员写凭证 ----

// 低权限操作者对数据范围内的高权限账号：改号、独立重置密码、更新用户时附带密码都被拒，库里的值没变。
func TestAdminCredentialWrite_PrivilegeCeiling(t *testing.T) {
	const oldMobile = "13800000001"

	type fixture struct {
		e                *credWriteEnv
		operator, target uint32
		lowRole, bigRole uint32
		opCtx            context.Context
	}
	setup := func(t *testing.T) *fixture {
		e := newCredWriteEnv(t)
		lowRole := e.userSvcEn.seedRoleWithPerms(t, "CRED_LOW", []uint32{11})
		bigRole := e.userSvcEn.seedRoleWithPerms(t, "CRED_BIG", []uint32{11, 22})
		operator := e.seedUser(t, "operator", "13700000007", lowRole)
		target := e.seedUser(t, "target", oldMobile, bigRole)
		return &fixture{e: e, operator: operator, target: target, lowRole: lowRole, bigRole: bigRole,
			opCtx: opCtx(e.sys, operator, 0, false)}
	}
	requireUntouched := func(t *testing.T, f *fixture) {
		t.Helper()
		require.Equal(t, oldMobile, f.e.mobile(t, f.target))
		require.Equal(t, "old-nick", f.e.nickname(t, f.target))
		_, err := f.e.userSvcEn.findCred(t, 0, "target", constants.DefaultUserPassword)
		require.NoError(t, err, "目标用户的密码不应被改动")
		roleIDs, err := f.e.users.userRepo.ListRoleIDsByUserID(f.e.sys, f.target)
		require.NoError(t, err)
		require.Equal(t, []uint32{f.bigRole}, roleIDs, "目标用户的角色不应被改动")
	}

	t.Run("改号被拒", func(t *testing.T) {
		f := setup(t)
		for name, req := range map[string]*identityV1.UpdateUserRequest{
			"带 mask": {
				Id:         f.target,
				Data:       &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("new-nick"), RoleIds: []uint32{f.bigRole}},
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile", "nickname"}},
			},
			"全量请求": {
				Id:   f.target,
				Data: &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("new-nick"), RoleIds: []uint32{f.bigRole}},
			},
			"清空": {
				Id:         f.target,
				Data:       &identityV1.User{Nickname: trans.Ptr("new-nick"), RoleIds: []uint32{f.bigRole}},
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile", "nickname"}},
			},
		} {
			_, err := f.e.users.Update(f.opCtx, req)
			require.True(t, adminV1.IsForbidden(err), "%s: got %v", name, err)
			requireUntouched(t, f)
		}
	})

	t.Run("号码没变或掩码回传时，改别的字段不受影响", func(t *testing.T) {
		f := setup(t)
		appAT := f.e.issue(t, authenticationV1.ClientType_app, f.target)
		for _, mobile := range []string{oldMobile, "138****0001"} {
			_, err := f.e.users.Update(f.opCtx, &identityV1.UpdateUserRequest{
				Id:         f.target,
				Data:       &identityV1.User{Mobile: trans.Ptr(mobile), Nickname: trans.Ptr("nick-" + mobile), RoleIds: []uint32{f.bigRole}},
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile", "nickname"}},
			})
			require.NoError(t, err, mobile)
			require.Equal(t, oldMobile, f.e.mobile(t, f.target))
			require.Equal(t, "nick-"+mobile, f.e.nickname(t, f.target))
		}
		require.True(t, f.e.tokenValid(appAT), "号码没有实际变化，会话不动")
	})

	t.Run("独立重置密码被拒", func(t *testing.T) {
		f := setup(t)
		_, err := f.e.users.EditUserPassword(f.opCtx, &identityV1.EditUserPasswordRequest{
			UserId: f.target, NewPassword: encryptLoginPassword(t, "HackedP@ss888"),
		})
		require.True(t, adminV1.IsForbidden(err), "got %v", err)
		requireUntouched(t, f)
	})

	t.Run("更新用户时附带密码被拒，字段和角色也不写", func(t *testing.T) {
		f := setup(t)
		adminAT := f.e.issue(t, authenticationV1.ClientType_admin, f.target)
		_, err := f.e.users.Update(f.opCtx, &identityV1.UpdateUserRequest{
			Id: f.target,
			// 同一个请求里把目标的角色降成操作者管得了的，再附带密码：按原有角色判定，照样拒绝。
			Data:       &identityV1.User{Nickname: trans.Ptr("new-nick"), RoleIds: []uint32{f.lowRole}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nickname", "role_ids"}},
			Password:   trans.Ptr(encryptLoginPassword(t, "HackedP@ss888")),
		})
		require.True(t, adminV1.IsForbidden(err), "got %v", err)
		requireUntouched(t, f)
		require.True(t, f.e.tokenValid(adminAT), "被拒绝的请求不应吊销目标用户的会话")
	})

	t.Run("同级或更低的账号可以改号和重置密码", func(t *testing.T) {
		f := setup(t)
		peer := f.e.seedUser(t, "peer", "13600000006", f.lowRole)
		_, err := f.e.users.Update(f.opCtx, &identityV1.UpdateUserRequest{
			Id:         peer,
			Data:       &identityV1.User{Mobile: trans.Ptr("13900000002"), RoleIds: []uint32{f.lowRole}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}},
		})
		require.NoError(t, err)
		require.Equal(t, "13900000002", f.e.mobile(t, peer))

		_, err = f.e.users.EditUserPassword(f.opCtx, &identityV1.EditUserPasswordRequest{
			UserId: peer, NewPassword: encryptLoginPassword(t, "PeerNewP@ss66"),
		})
		require.NoError(t, err)
		_, err = f.e.userSvcEn.findCred(t, 0, "peer", "PeerNewP@ss66")
		require.NoError(t, err)

		_, err = f.e.users.Update(f.opCtx, &identityV1.UpdateUserRequest{
			Id:         peer,
			Data:       &identityV1.User{RoleIds: []uint32{f.lowRole}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nickname"}},
			Password:   trans.Ptr(encryptLoginPassword(t, "PeerNewP@ss77")),
		})
		require.NoError(t, err)
		_, err = f.e.userSvcEn.findCred(t, 0, "peer", "PeerNewP@ss77")
		require.NoError(t, err)
	})

	t.Run("平台管理员不受限", func(t *testing.T) {
		f := setup(t)
		platform := opCtx(f.e.sys, f.operator, 0, true)
		_, err := f.e.users.Update(platform, &identityV1.UpdateUserRequest{
			Id:         f.target,
			Data:       &identityV1.User{Mobile: trans.Ptr("13900000002"), RoleIds: []uint32{f.bigRole}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}},
		})
		require.NoError(t, err)
		require.Equal(t, "13900000002", f.e.mobile(t, f.target))
	})
}

// 管理员改号之后 app 会话失效、管理端会话不动；号码没有实际变化时什么都不做。
func TestAdminMobileChange_RevokesAppSessions(t *testing.T) {
	e := newCredWriteEnv(t)
	role := e.userSvcEn.seedRoleWithPerms(t, "CRED_ANY", []uint32{11})
	operator := e.seedUser(t, "operator", "13700000007", role)
	target := e.seedUser(t, "target", "13800000001", role)
	ctx := opCtx(e.sys, operator, 0, true)

	update := func(data *identityV1.User, paths ...string) error {
		data.RoleIds = []uint32{role}
		_, err := e.users.Update(ctx, &identityV1.UpdateUserRequest{
			Id: target, Data: data, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
		})
		return err
	}

	// 改号：app 会话失效，管理端会话不动。
	appAT := e.issue(t, authenticationV1.ClientType_app, target)
	adminAT := e.issue(t, authenticationV1.ClientType_admin, target)
	require.NoError(t, update(&identityV1.User{Mobile: trans.Ptr("13900000002")}, "mobile"))
	require.Equal(t, "13900000002", e.mobile(t, target))
	require.False(t, e.tokenValid(appAT))
	require.True(t, e.tokenValid(adminAT))

	// 号码和库里一致、掩码回传：都不算变更，app 会话不动。
	appAT = e.issue(t, authenticationV1.ClientType_app, target)
	require.NoError(t, update(&identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("n1")}, "mobile", "nickname"))
	require.NoError(t, update(&identityV1.User{Mobile: trans.Ptr("139****0002"), Nickname: trans.Ptr("n2")}, "mobile", "nickname"))
	require.Equal(t, "13900000002", e.mobile(t, target))
	require.Equal(t, "n2", e.nickname(t, target))
	require.True(t, e.tokenValid(appAT))

	// Redis 删除 app 令牌失败：数据库已提交，接口返回 503；没删掉的令牌在有效期内仍能用（接受的残留）。
	e.fault.set(func(name, key string) bool { return name == "del" && strings.HasPrefix(key, "at:1:") })
	err := update(&identityV1.User{Mobile: trans.Ptr("13900000003")}, "mobile")
	require.True(t, adminV1.IsServiceUnavailable(err), "got %v", err)
	require.Equal(t, "13900000003", e.mobile(t, target))
	require.True(t, e.tokenValid(appAT))
	// 原样重试：号码已经一致，直接成功，什么都不做。
	e.fault.set(nil)
	require.NoError(t, update(&identityV1.User{Mobile: trans.Ptr("13900000003")}, "mobile"))
	require.True(t, e.tokenValid(appAT))

	// 清空号码同样吊销 app 会话。
	require.NoError(t, update(&identityV1.User{}, "mobile"))
	require.Equal(t, "", e.mobile(t, target))
	require.False(t, e.tokenValid(appAT))
}
