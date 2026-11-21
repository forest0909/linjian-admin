package service

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

// MiniappAuthService 微信小程序登录：一键登录、静默续期、退出，以及管理端查看和解除绑定。
// 目前只有接口合同，所有方法都返回「未实现」。
type MiniappAuthService struct {
	adminV1.MiniappAuthServiceHTTPServer

	log *bLogger.Helper
}

func NewMiniappAuthService(ctx *bootstrap.Context) *MiniappAuthService {
	return &MiniappAuthService{
		log: ctx.NewLoggerHelper("miniapp-auth/service/admin-service"),
	}
}

func errMiniappNotImplemented() error {
	return authenticationV1.ErrorNotImplemented("miniapp auth is not implemented")
}

// LoginByPhone 手机号一键登录。
func (s *MiniappAuthService) LoginByPhone(_ context.Context, _ *authenticationV1.MiniappPhoneLoginRequest) (*authenticationV1.LoginResponse, error) {
	return nil, errMiniappNotImplemented()
}

// SilentLogin 静默续期。
func (s *MiniappAuthService) SilentLogin(_ context.Context, _ *authenticationV1.MiniappSilentLoginRequest) (*authenticationV1.LoginResponse, error) {
	return nil, errMiniappNotImplemented()
}

// Logout 小程序退出。
func (s *MiniappAuthService) Logout(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
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
