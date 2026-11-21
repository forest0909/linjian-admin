package service

import (
	"context"
	"errors"

	ktransport "github.com/go-kratos/kratos/v2/transport"
	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/app/admin/service/internal/data"

	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/middleware/logging"
	"go-wind-admin/pkg/netutil"
	"go-wind-admin/pkg/phone"
)

// MiniappAuthService 微信小程序登录：一键登录、静默续期、退出，以及管理端查看和解除绑定。
//
// 小程序令牌的内容和管理端一样，走同一条中间件；长期凭证是微信绑定（openid → 用户）本身：
// 续期请求只带新的 wx.login code，服务端按 openid 找到绑定和用户就签发。
// 账号密码登录不在这里，复用 /admin/v1/login（client_type=app）。
type MiniappAuthService struct {
	adminV1.MiniappAuthServiceHTTPServer

	log *bLogger.Helper

	// miniapp 微信配置、客户端和请求额度；未启用时为 nil。
	miniapp *MiniappRuntime

	// authn 提供权限计算（resolveUserAuthority）和 app 会话签发（issueAppSession），和密码登录共用。
	authn              *AuthenticationService
	userRepo           data.UserRepo
	userCredentialRepo *data.UserCredentialRepo
}

func NewMiniappAuthService(
	ctx *bootstrap.Context,
	miniapp *MiniappRuntime,
	authn *AuthenticationService,
	userRepo data.UserRepo,
	userCredentialRepo *data.UserCredentialRepo,
) *MiniappAuthService {
	return &MiniappAuthService{
		log:                ctx.NewLoggerHelper("miniapp-auth/service/admin-service"),
		miniapp:            miniapp,
		authn:              authn,
		userRepo:           userRepo,
		userCredentialRepo: userCredentialRepo,
	}
}

func errMiniappNotImplemented() error {
	return authenticationV1.ErrorNotImplemented("miniapp auth is not implemented")
}

func errMiniappUnavailable() error {
	return authenticationV1.ErrorMiniappUnavailable("miniapp login is temporarily unavailable")
}

// setAuditUsername 登录审计要的用户名：请求体里没有，经响应头回传给审计中间件。
func setAuditUsername(ctx context.Context, username string) {
	if username == "" {
		return
	}
	if tr, ok := ktransport.FromServerContext(ctx); ok {
		if header := tr.ReplyHeader(); header != nil {
			header.Set("X-Audit-Username", username)
		}
	}
}

// takeRequestQuota 第一阶段额度（客户端 IP、AppID 总量），调微信之前扣。
// 超额返回 429，额度用的 Redis 出错返回 503；两种情况这个请求都不会调微信。
func (s *MiniappAuthService) takeRequestQuota(ctx context.Context) error {
	allowed, err := s.miniapp.Quota.AllowRequest(ctx, netutil.ClientIPFromContext(ctx), s.miniapp.AppID)
	if err != nil {
		return errMiniappUnavailable()
	}
	if !allowed {
		return authenticationV1.ErrorTooManyRequests("too many requests, please try again later")
	}
	return nil
}

// loadUserForIssue 读用户行并确认它属于租户 0。userID 来自已验证的手机号匹配或绑定行。
func (s *MiniappAuthService) loadUserForIssue(ctx context.Context, userID uint32) (*identityV1.User, error) {
	user, err := s.userRepo.Get(ctx, &identityV1.GetUserRequest{QueryBy: &identityV1.GetUserRequest_Id{Id: userID}})
	if err != nil {
		s.log.Errorf(ctx, "miniapp login: get user [%d] failed: %s", userID, err.Error())
		return nil, errMiniappUnavailable()
	}
	if user.GetTenantId() != 0 {
		return nil, authenticationV1.ErrorForbidden("user is disabled")
	}
	return user, nil
}

// LoginByPhone 手机号一键登录。两个 code 都只由服务端向微信兑换；请求里没有 openid 和手机号。
// 失败合同见设计方案 5.6：手机号路径没有别的认证依据，绑定写不进去就不签发，让用户重试。
func (s *MiniappAuthService) LoginByPhone(ctx context.Context, req *authenticationV1.MiniappPhoneLoginRequest) (*authenticationV1.LoginResponse, error) {
	if !s.miniapp.Enabled() {
		return nil, authenticationV1.ErrorMiniappUnavailable("miniapp login is not enabled")
	}
	ctx = s.authn.resetContextForLogin(ctx)

	if err := s.takeRequestQuota(ctx); err != nil {
		return nil, err
	}

	openid, err := s.miniapp.Client.Code2Session(ctx, req.GetCode())
	if err != nil {
		// err 是 *wechat.Error，里面没有 code、Secret 和 URL。
		s.log.Warnf(ctx, "miniapp phone login: %v", err)
		return nil, wechatLoginError(err)
	}

	// 第二阶段额度：换手机号按次收费，按 openid 限次。扣不到就不取 access_token、不调换号接口。
	allowed, err := s.miniapp.Quota.AllowPhoneExchange(ctx, openid)
	if err != nil {
		return nil, errMiniappUnavailable()
	}
	if !allowed {
		s.log.Warnf(ctx, "miniapp phone login: phone exchange quota exceeded for openid [%s]", maskOpenID(openid))
		return nil, authenticationV1.ErrorTooManyRequests("too many requests, please try again later")
	}

	wxPhone, err := s.miniapp.Client.GetPhoneNumber(ctx, req.GetPhoneCode())
	if err != nil {
		s.log.Warnf(ctx, "miniapp phone login: %v", err)
		return nil, wechatLoginError(err)
	}

	// 第一期只接受中国大陆 11 位号码，其他按未开通处理。
	mobile, ok := phone.NormalizeCNMobile(wxPhone.PurePhoneNumber)
	if wxPhone.CountryCode != "86" || !ok {
		return nil, authenticationV1.ErrorMiniappPhoneNotRegistered("phone is not registered")
	}

	// 按手机号精确匹配：0 行拒绝，多行拒绝，1 行继续。不自动注册。
	userIDs, err := s.userRepo.FindUserIDsByMobile(ctx, mobile)
	if err != nil {
		return nil, errMiniappUnavailable()
	}
	switch len(userIDs) {
	case 0:
		s.log.Warnf(ctx, "miniapp phone login: no account for mobile [%s]", phone.Mask(mobile))
		return nil, authenticationV1.ErrorMiniappPhoneNotRegistered("phone is not registered")
	case 1:
	default:
		s.log.Warnf(ctx, "miniapp phone login: mobile [%s] matches %d accounts", phone.Mask(mobile), len(userIDs))
		return nil, authenticationV1.ErrorMiniappPhoneAmbiguous("phone matches multiple accounts")
	}
	userID := userIDs[0]

	// 确定用户后先读撤销版本，签发时按它条件写入。
	revokeVersion, err := s.authn.authenticator.ReadRevokeVersion(ctx, authenticationV1.ClientType_app, userID)
	if err != nil {
		return nil, errMiniappUnavailable()
	}

	// 绑定事务：锁用户行，确认用户启用、手机号仍相等，确认 openid 没绑别人，写入绑定（已有则不动）。
	bindingID, err := s.userCredentialRepo.BindMiniapp(ctx, userID, openid, data.MiniappBindCheck{Mobile: &mobile})
	switch {
	case err == nil:
	case errors.Is(err, data.ErrMiniappBoundToOther):
		s.log.Warnf(ctx, "miniapp phone login: openid [%s] is bound to another user, user [%d] rejected", maskOpenID(openid), userID)
		return nil, authenticationV1.ErrorMiniappBoundToOther("this wechat is bound to another account")
	case errors.Is(err, data.ErrMiniappUserDisabled):
		return nil, authenticationV1.ErrorForbidden("user is disabled")
	default:
		// 事务失败，或手机号在匹配之后变了：没有绑定，不签发，可重试。
		s.log.Errorf(ctx, "miniapp phone login: bind openid [%s] to user [%d] failed: %v", maskOpenID(openid), userID, err)
		return nil, errMiniappUnavailable()
	}

	user, err := s.loadUserForIssue(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.issue(ctx, user, bindingID, revokeVersion, true)
}

// SilentLogin 静默续期。请求只带新的 wx.login code；这个微信有绑定就给绑定的用户签发，不写绑定。
// 权限每次重新计算。
func (s *MiniappAuthService) SilentLogin(ctx context.Context, req *authenticationV1.MiniappSilentLoginRequest) (*authenticationV1.LoginResponse, error) {
	if !s.miniapp.Enabled() {
		return nil, authenticationV1.ErrorMiniappUnavailable("miniapp login is not enabled")
	}
	ctx = s.authn.resetContextForLogin(ctx)

	if err := s.takeRequestQuota(ctx); err != nil {
		return nil, err
	}

	openid, err := s.miniapp.Client.Code2Session(ctx, req.GetCode())
	if err != nil {
		s.log.Warnf(ctx, "miniapp silent login: %v", err)
		return nil, wechatLoginError(err)
	}

	binding, err := s.userCredentialRepo.FindMiniappBinding(ctx, openid)
	if err != nil {
		return nil, errMiniappUnavailable()
	}
	if binding == nil {
		return nil, authenticationV1.ErrorMiniappNotBound("this wechat is not bound")
	}

	revokeVersion, err := s.authn.authenticator.ReadRevokeVersion(ctx, authenticationV1.ClientType_app, binding.UserID)
	if err != nil {
		return nil, errMiniappUnavailable()
	}

	// 读完版本之后重新读绑定：它还在、还属于同一个用户才签发。
	// 绑定在这之间被删（下线、改密、改号）的话，要么这里读不到，要么撤销推进了版本、下面写不进去。
	current, err := s.userCredentialRepo.FindMiniappBinding(ctx, openid)
	if err != nil {
		return nil, errMiniappUnavailable()
	}
	if current == nil || current.ID != binding.ID || current.UserID != binding.UserID {
		return nil, authenticationV1.ErrorMiniappNotBound("this wechat is not bound")
	}

	user, err := s.loadUserForIssue(ctx, binding.UserID)
	if err != nil {
		return nil, err
	}
	return s.issue(ctx, user, binding.ID, revokeVersion, false)
}

// issue 计算权限并按撤销版本条件签发。audit 为 true 时把已验证的身份和用户名交给登录审计
// （一键登录进登录审计；静默续期不进，和管理端刷新一样）。
func (s *MiniappAuthService) issue(ctx context.Context, user *identityV1.User, bindingID uint32, revokeVersion int64, audit bool) (*authenticationV1.LoginResponse, error) {
	tokenPayload := &authenticationV1.UserTokenPayload{
		UserId:   user.GetId(),
		TenantId: user.TenantId,
		Username: user.Username,
	}
	if err := s.authn.resolveUserAuthority(ctx, user, tokenPayload); err != nil {
		s.log.Errorf(ctx, "miniapp login: resolve user [%d] authority failed [%s]", user.GetId(), err.Error())
		return nil, err
	}

	accessToken, err := s.authn.issueAppSession(ctx, tokenPayload, bindingID, revokeVersion)
	if err != nil {
		return nil, err
	}

	if audit {
		logging.SetAuditIdentity(ctx, tokenPayload)
		setAuditUsername(ctx, user.GetUsername())
		recordUserLastLogin(ctx, s.log, s.userRepo, user.GetId(), netutil.ClientIPFromContext(ctx))
	}
	return s.authn.appLoginResponse(accessToken, true), nil
}

// Logout 小程序退出。只收 app 令牌；退出本身还没实现。
func (s *MiniappAuthService) Logout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if operator.GetClientType() != authenticationV1.ClientType_app {
		return nil, authenticationV1.ErrorBadRequest("miniapp logout only accepts app token")
	}
	return nil, errMiniappNotImplemented()
}

// ListUserBindings 查询用户的微信小程序绑定。
func (s *MiniappAuthService) ListUserBindings(_ context.Context, _ *authenticationV1.ListMiniappBindingsRequest) (*authenticationV1.ListMiniappBindingsResponse, error) {
	return nil, errMiniappNotImplemented()
}

// DeleteUserBinding 解除用户的微信小程序绑定。
func (s *MiniappAuthService) DeleteUserBinding(_ context.Context, _ *authenticationV1.DeleteMiniappBindingRequest) (*emptypb.Empty, error) {
	return nil, errMiniappNotImplemented()
}
