package service

import (
	"context"
	"errors"
	"strings"

	"go-wind-admin/third_party/go-utils/trans"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/app/admin/service/internal/data"

	"go-wind-admin/pkg/wechat"
)

// 小程序（client_type=app）会话的签发。三条登录路径共用 issueAppSession，顺序固定：
//
//  1. 确定用户 ID 后，先读撤销版本 rv:{app}:{uid}，记为 v；
//  2. 认证、绑定事务、读用户状态、计算权限；
//  3. 按版本 v 条件写入 access token 和会话元数据。版本变了就不写，返回 CONFLICT。
//
// 撤销（改密、改号、下线、解绑）先提交数据库，再推进版本并删令牌。两边合起来：
// 请求先读了版本、撤销随后完成、请求再去签发，版本对不上，写不进去；
// 撤销的数据库部分先提交、请求随后才认证，读到的已经是新状态。
// 签发失败时已写的绑定保留，没有补偿删除：绑定是在认证成立并通过事务内重新确认之后写的，本身合法。

// maskOpenID 日志里的 openid 只留前 6 后 4。
func maskOpenID(openid string) string {
	if len(openid) <= 10 {
		return strings.Repeat("*", len(openid))
	}
	return openid[:6] + "****" + openid[len(openid)-4:]
}

// issueAppSession 按撤销版本条件写入 app 令牌和会话元数据。bindingID 是这次会话所用的绑定行，
// H5 登录和没建成绑定的登录为 0。只有 access token：不签发 refresh token，不设 Cookie。
func (s *AuthenticationService) issueAppSession(ctx context.Context, tokenPayload *authenticationV1.UserTokenPayload,
	bindingID uint32, revokeVersion int64) (string, error) {
	meta := newSessionMeta(ctx, tokenPayload, 0)
	meta.BindingID = bindingID

	accessToken, err := s.authenticator.CreateAppSession(ctx, tokenPayload, meta, revokeVersion)
	if err != nil {
		if errors.Is(err, data.ErrSessionRevoked) {
			s.log.Warnf(ctx, "app session of user [%d] revoked during login, token discarded", tokenPayload.GetUserId())
			return "", authenticationV1.ErrorConflict("session was revoked during login, please retry")
		}
		s.log.Errorf(ctx, "write app session failed for user [%d]: %v", tokenPayload.GetUserId(), err)
		return "", authenticationV1.ErrorMiniappUnavailable("miniapp login is temporarily unavailable")
	}
	return accessToken, nil
}

// appLoginResponse app 登录的响应。miniapp_bound 为 true 表示这个微信到本用户的绑定已在库里，
// 令牌过期后可以静默续期。
func (s *AuthenticationService) appLoginResponse(accessToken string, bound bool) *authenticationV1.LoginResponse {
	return &authenticationV1.LoginResponse{
		TokenType:    authenticationV1.TokenType_bearer,
		AccessToken:  accessToken,
		ExpiresIn:    int64(s.authenticator.GetAccessTokenExpires(authenticationV1.ClientType_app).Seconds()),
		MiniappBound: trans.Ptr(bound),
	}
}

// finishAppPasswordLogin 小程序账号密码登录在密码校验通过之后的部分：
// 可选的微信绑定、计算权限、按撤销版本条件签发。不下发 refresh Cookie。
func (s *AuthenticationService) finishAppPasswordLogin(ctx context.Context, req *authenticationV1.LoginRequest,
	user *identityV1.User, tokenPayload *authenticationV1.UserTokenPayload,
	passwordSnapshot string, revokeVersion int64, clientIP, username string) (*authenticationV1.LoginResponse, error) {
	// 请求带了 wx.login 的 code 才绑定；H5 下不带。
	var bindingID uint32
	if code := strings.TrimSpace(req.GetCode()); code != "" {
		var err error
		if bindingID, err = s.bindOnPasswordLogin(ctx, user.GetId(), code, passwordSnapshot, clientIP); err != nil {
			return nil, err
		}
	}

	if err := s.resolveUserAuthority(ctx, user, tokenPayload); err != nil {
		s.log.Errorf(ctx, "resolve user [%d] authority failed [%s]", user.GetId(), err.Error())
		return nil, err
	}

	accessToken, err := s.issueAppSession(ctx, tokenPayload, bindingID, revokeVersion)
	if err != nil {
		return nil, err
	}

	recordUserLastLogin(ctx, s.log, s.userRepo, user.GetId(), clientIP)
	if s.rateLimiter != nil {
		s.rateLimiter.Reset(ctx, clientIP, username)
	}
	return s.appLoginResponse(accessToken, bindingID != 0), nil
}

// bindOnPasswordLogin 密码已经校验通过，绑定只是附加能力：超额、Redis 故障、微信不可用、
// code 无效、绑定事务失败都降级为不绑定（返回 0, nil），登录继续，miniapp_bound=false。
// 只有确认这个微信已经绑定了别的账号时才拒绝登录。
func (s *AuthenticationService) bindOnPasswordLogin(ctx context.Context, userID uint32, code, passwordSnapshot, clientIP string) (uint32, error) {
	// 额度在调微信之前扣；超额或额度用的 Redis 出错都不调微信。
	allowed, err := s.miniapp.Quota.AllowRequest(ctx, clientIP, s.miniapp.AppID)
	if err != nil {
		s.log.Errorf(ctx, "app login: quota check failed, skip binding for user [%d]: %v", userID, err)
		return 0, nil
	}
	if !allowed {
		s.log.Warnf(ctx, "app login: wechat quota exceeded, skip binding for user [%d]", userID)
		return 0, nil
	}

	openid, err := s.miniapp.Client.Code2Session(ctx, code)
	if err != nil {
		// err 是 *wechat.Error，里面没有 code、Secret 和 URL。
		s.log.Warnf(ctx, "app login: code2session failed, skip binding for user [%d]: %v", userID, err)
		return 0, nil
	}

	bindingID, err := s.userCredentialRepo.BindMiniapp(ctx, userID, openid, data.MiniappBindCheck{PasswordSnapshot: &passwordSnapshot})
	if err != nil {
		if errors.Is(err, data.ErrMiniappBoundToOther) {
			s.log.Warnf(ctx, "app login: openid [%s] is bound to another user, login of user [%d] rejected", maskOpenID(openid), userID)
			return 0, authenticationV1.ErrorMiniappBoundToOther("this wechat is bound to another account")
		}
		s.log.Errorf(ctx, "app login: bind openid [%s] to user [%d] failed, continue without binding: %v", maskOpenID(openid), userID, err)
		return 0, nil
	}
	return bindingID, nil
}

// wechatLoginError 把微信适配器的错误换成登录接口的错误：code 本身不行是 400，其余是可重试的 503。
func wechatLoginError(err error) error {
	if wechat.IsCodeInvalid(err) {
		return authenticationV1.ErrorMiniappCodeInvalid("wechat authorization expired")
	}
	return authenticationV1.ErrorMiniappUnavailable("miniapp login is temporarily unavailable")
}
