package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"go-wind-admin/third_party/go-utils/trans"
	"google.golang.org/protobuf/types/known/emptypb"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/pkg/mailtext"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/phone"
)

// 联系方式种类：绑定验证码按它分渠道（邮箱走 SMTP，手机号走 SMS）。
const (
	contactKindEmail = "email"
	contactKindPhone = "phone"
)

const (
	bindContactPurpose = "bind_contact"
	bindContactCodeTTL = 10 * time.Minute
)

// contactSender 验证码的发送出口（生产是 *OutboundSender）。
type contactSender interface {
	SMSConfigured() bool
	SendSMS(ctx context.Context, phone, code string) error
	SendMail(ctx context.Context, to, subject, body string) error
}

// generateVCode 生成 6 位数字验证码。
// 手机号是登录凭证，改号验证码用密码学随机数，不能从时间推出来。
func generateVCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// maskContact 日志里的联系方式一律打码。
func maskContact(kind, contact string) string {
	if kind == contactKindPhone {
		return phone.Mask(contact)
	}
	return phone.MaskEmail(contact)
}

// sendContactVCode 向指定联系方式发送业务验证码（绑定场景）。
// 邮箱走 mail.yaml 的 SMTP，手机号走同一文件里的短信账号。
// 验证码归属到申请它的用户；发送有额度；日志里没有验证码，联系方式打码。
func (s *UserProfileService) sendContactVCode(ctx context.Context, userID uint32, kind, contact string) error {
	// 短信渠道没配置时直接说明，不占额度：页面据此提示找管理员代改。
	if kind == contactKindPhone && !s.outbound.SMSConfigured() {
		return authenticationV1.ErrorNotImplemented("sms channel is not configured")
	}

	allowed, err := s.vcodeCache.AllowSendForUser(ctx, userID, contact)
	if err != nil {
		return authenticationV1.ErrorServiceUnavailable("verification code service unavailable")
	}
	if !allowed {
		return authenticationV1.ErrorTooManyRequests("too many verification code requests")
	}

	code, err := generateVCode()
	if err != nil {
		s.log.Errorf(ctx, "bind-contact: generate code failed: %v", err)
		return authenticationV1.ErrorInternalServerError("generate verification code failed")
	}
	if err = s.vcodeCache.SaveForUser(ctx, userID, bindContactPurpose, contact, code, bindContactCodeTTL); err != nil {
		return authenticationV1.ErrorServiceUnavailable("verification code service unavailable")
	}

	// 文案语言：绑定场景操作者即收件人，读其偏好 locale（未设置回落请求头判定）。
	if user, uErr := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{Id: userID},
	}); uErr == nil && user.GetLocale() != "" {
		if loc, ok := mailtext.LocaleOfTag(user.GetLocale()); ok {
			ctx = mailtext.WithLocale(ctx, loc)
		}
	}

	switch kind {
	case contactKindPhone:
		err = s.outbound.SendSMS(ctx, contact, code)
	default:
		title, content := renderContactBindCode(ctx, s.mailer, code)
		err = s.outbound.SendMail(ctx, contact, title, content)
	}
	if err != nil {
		s.log.Errorf(ctx, "bind-contact: send %s code to [%s] for user [%d] failed: %s", kind, maskContact(kind, contact), userID, err.Error())
		if errors.Is(err, ErrOutboundNotConfigured) {
			if kind == contactKindPhone {
				return authenticationV1.ErrorNotImplemented("sms channel is not configured")
			}
			return authenticationV1.ErrorInternalServerError("email channel is not configured")
		}
		return authenticationV1.ErrorInternalServerError("send verification message failed")
	}
	return nil
}

// BindContact 绑定手机号码/邮箱（第一步）：向新联系方式发送验证码。
// 手机号按统一规范收成 11 位纯数字（第一期只接受中国大陆号码）。
func (s *UserProfileService) BindContact(ctx context.Context, req *identityV1.BindContactRequest) (*emptypb.Empty, error) {
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	if bindPhone := req.GetPhone(); bindPhone != nil {
		if strings.TrimSpace(bindPhone.GetPhone()) == "" {
			return nil, authenticationV1.ErrorBadRequest("phone is required")
		}
		// 服务入口先做一次格式校验：错误在第一步就回给用户。
		mobile, ok := phone.NormalizeCNMobile(bindPhone.GetPhone())
		if !ok {
			return nil, authenticationV1.ErrorBadRequest("invalid phone number")
		}
		if err = s.sendContactVCode(ctx, operator.GetUserId(), contactKindPhone, mobile); err != nil {
			return nil, err
		}
		return &emptypb.Empty{}, nil
	}

	if bindEmail := req.GetEmail(); bindEmail != nil {
		contact := strings.TrimSpace(bindEmail.GetEmail())
		if contact == "" {
			return nil, authenticationV1.ErrorBadRequest("email is required")
		}
		if err = s.sendContactVCode(ctx, operator.GetUserId(), contactKindEmail, contact); err != nil {
			return nil, err
		}
		return &emptypb.Empty{}, nil
	}

	return nil, authenticationV1.ErrorBadRequest("contact is required")
}

// VerifyContact 校验验证码并完成联系方式绑定。
// 手机号：验证通过后写成用户的登录号码（sys_users.mobile），见 verifyPhone。
// 邮箱：写一行 EMAIL 凭证，identifier=邮箱；credential 为占位哈希——该凭证不用于密码校验。
func (s *UserProfileService) VerifyContact(ctx context.Context, req *identityV1.VerifyContactRequest) (*emptypb.Empty, error) {
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	var kind, contact, code string
	if verifyPhone := req.GetPhone(); verifyPhone != nil {
		kind, contact, code = contactKindPhone, strings.TrimSpace(verifyPhone.GetPhone()), strings.TrimSpace(verifyPhone.GetCode())
	} else if verifyEmail := req.GetEmail(); verifyEmail != nil {
		kind, contact, code = contactKindEmail, strings.TrimSpace(verifyEmail.GetEmail()), strings.TrimSpace(verifyEmail.GetCode())
	}
	if contact == "" || code == "" {
		return nil, authenticationV1.ErrorBadRequest("contact and code are required")
	}
	if kind == contactKindPhone {
		mobile, ok := phone.NormalizeCNMobile(contact)
		if !ok {
			return nil, authenticationV1.ErrorBadRequest("invalid phone number")
		}
		contact = mobile
	}

	// 验证码归属到申请它的用户；校验、消费、计错是一次原子操作。Redis 出错时拒绝，不放行。
	ok, err := s.vcodeCache.VerifyForUser(ctx, operator.GetUserId(), bindContactPurpose, contact, code)
	if err != nil {
		return nil, authenticationV1.ErrorServiceUnavailable("verification code service unavailable")
	}
	if !ok {
		return nil, authenticationV1.ErrorBadRequest("invalid or expired verification code")
	}

	if kind == contactKindPhone {
		return s.verifyPhone(ctx, operator.GetUserId(), contact)
	}

	if err = s.userCredentialRepo.Create(ctx, &authenticationV1.CreateUserCredentialRequest{
		Data: &authenticationV1.UserCredential{
			TenantId:       trans.Ptr(operator.GetTenantId()),
			UserId:         trans.Ptr(operator.GetUserId()),
			IdentityType:   authenticationV1.UserCredential_EMAIL.Enum(),
			Identifier:     trans.Ptr(contact),
			CredentialType: authenticationV1.UserCredential_PASSWORD_HASH.Enum(),
			Credential:     trans.Ptr(dummyPasswordHash),
			IsPrimary:      trans.Ptr(false),
			Status:         authenticationV1.UserCredential_ENABLED.Enum(),
		},
	}); err != nil {
		s.log.Errorf(ctx, "bind contact: create EMAIL credential failed for user [%d]: %s", operator.GetUserId(), err.Error())
		return nil, err
	}

	s.log.Infof(ctx, "user [%d] bound %s [%s]", operator.GetUserId(), kind, maskContact(kind, contact))
	return &emptypb.Empty{}, nil
}

// verifyPhone 验证码通过后的改号。数据库部分在一个事务里（见 BindVerifiedPhone）；
// 提交后吊销该用户的 app 令牌：旧号码建立的小程序会话不能再用。
// 新旧号码相同时直接返回成功，什么都不做。
func (s *UserProfileService) verifyPhone(ctx context.Context, userID uint32, mobile string) (*emptypb.Empty, error) {
	changed, err := s.userCredentialRepo.BindVerifiedPhone(ctx, userID, mobile)
	if err != nil {
		s.log.Errorf(ctx, "bind contact: change mobile failed for user [%d]: %s", userID, err.Error())
		return nil, err
	}
	if !changed {
		return &emptypb.Empty{}, nil
	}

	if err = s.authenticator.RevokeUserToken(ctx, authenticationV1.ClientType_app, userID); err != nil {
		// 号码已经改了，但旧的小程序令牌没清干净：已签发的 app 令牌在自己的有效期内还能用。
		s.log.Errorf(ctx, "bind contact: revoke app tokens failed for user [%d]: %v", userID, err)
		return nil, authenticationV1.ErrorServiceUnavailable("mobile updated but session revocation failed")
	}

	s.log.Infof(ctx, "user [%d] changed mobile to [%s]", userID, phone.Mask(mobile))
	return &emptypb.Empty{}, nil
}

// dummyPasswordHash 合法 bcrypt 占位（EMAIL 凭证不用于密码校验）
const dummyPasswordHash = "$2a$10$1sbpKmhQDpXLHnDnEQ1nLe3oOnYyP2bUJyqHcX2T0Fq1qfyoXOrPm"
