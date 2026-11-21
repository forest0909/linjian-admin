package data

import (
	"context"

	"go-wind-admin/third_party/go-utils/trans"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	"go-wind-admin/pkg/middleware/auth"
)

type TokenChecker struct {
	log *bLogger.Helper

	authenticator *Authenticator
}

func NewTokenChecker(
	ctx *bootstrap.Context,
	authenticator *Authenticator,
) auth.AccessTokenChecker {
	return &TokenChecker{
		log:           bLogger.NewHelper(ctx.GetLogger().With("module", "token-checker/auth/middleware")),
		authenticator: authenticator,
	}
}

// IsValidAccessToken checks if the access token is valid for the given user ID.
// 客户端类型取自令牌自己的 ct 声明（缺省 admin），返回的 payload 里 ClientType 一定有值。
func (tc *TokenChecker) IsValidAccessToken(ctx context.Context, accessToken string, skipRedis bool) (bool, *authenticationV1.UserTokenPayload) {
	resp, err := tc.authenticator.Authenticate(ctx, &authenticationV1.ValidateTokenRequest{
		Token:         accessToken,
		TokenCategory: authenticationV1.TokenCategory_ACCESS,
		SkipRedis:     trans.Ptr(skipRedis),
	})
	if err != nil {
		return false, nil
	}

	if !resp.IsValid {
		return false, nil
	}

	return true, resp.Payload
}

// IsBlockedAccessToken checks if the access token is blocked for the given user ID.
func (tc *TokenChecker) IsBlockedAccessToken(ctx context.Context, accessToken string) bool {
	resp, err := tc.authenticator.Authenticate(ctx, &authenticationV1.ValidateTokenRequest{
		Token:         accessToken,
		TokenCategory: authenticationV1.TokenCategory_ACCESS,
		SkipRedis:     trans.Ptr(true),
	})
	if err != nil {
		return true
	}
	return !resp.IsValid
}

// VerifyAccessToken 同 IsValidAccessToken，但把失败原因带出来：
// 令牌存储故障返回 503 错误，令牌本身无效返回 401 错误。鉴权中间件优先用它。
func (tc *TokenChecker) VerifyAccessToken(ctx context.Context, accessToken string) (*authenticationV1.UserTokenPayload, error) {
	resp, err := tc.authenticator.Authenticate(ctx, &authenticationV1.ValidateTokenRequest{
		Token:         accessToken,
		TokenCategory: authenticationV1.TokenCategory_ACCESS,
	})
	if err != nil {
		return nil, err
	}
	if !resp.GetIsValid() {
		return nil, authenticationV1.ErrorUnauthorized("invalid access token")
	}
	return resp.GetPayload(), nil
}
