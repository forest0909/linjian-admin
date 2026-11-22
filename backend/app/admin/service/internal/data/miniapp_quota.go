package data

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	"go-wind-admin/pkg/serviceid"
)

// 微信分支（一键登录、静默续期、带 code 的 app 密码登录）的请求额度。
// 成功和失败的请求都计数：这几个接口每次都要调微信，换手机号还按次收费。
// 额度是代码常量，都放在这里。
const (
	miniappQuotaKeyPrefix = serviceid.ProjectName + ":miniapp:quota:"

	// 第一阶段，调微信之前扣：客户端 IP 每分钟 60 次，AppID 总量每分钟 600 次。
	MiniappQuotaIPLimit   = 60
	MiniappQuotaIPWindow  = time.Minute
	MiniappQuotaAppLimit  = 600
	MiniappQuotaAppWindow = time.Minute
	// 第二阶段，拿到 openid 之后、取 access_token 和调换号接口之前扣：每个 openid 每 10 分钟 5 次。
	MiniappQuotaPhoneLimit  = 5
	MiniappQuotaPhoneWindow = 10 * time.Minute
)

// takeMiniappQuotaScript 原子检查并扣减若干个计数器，任何一个用完都不扣。
// KEYS: 各计数器的键；ARGV: 依次是每个键的 [limit, windowMs]。返回 1=放行，0=超额。
var takeMiniappQuotaScript = redis.NewScript(`
	for i, key in ipairs(KEYS) do
		local used = tonumber(redis.call('GET', key) or '0')
		if used >= tonumber(ARGV[i * 2 - 1]) then
			return 0
		end
	end
	for i, key in ipairs(KEYS) do
		if redis.call('INCR', key) == 1 then
			redis.call('PEXPIRE', key, ARGV[i * 2])
		end
	end
	return 1
`)

// MiniappQuota 微信分支的请求额度（Redis 固定窗口计数）。
type MiniappQuota struct {
	rdb *redis.Client
	log *bLogger.Helper
}

func NewMiniappQuota(ctx *bootstrap.Context, rdb *redis.Client) *MiniappQuota {
	return &MiniappQuota{rdb: rdb, log: ctx.NewLoggerHelper("miniapp-quota/data")}
}

// AllowRequest 第一阶段：按客户端 IP 和 AppID 总量扣减，调微信之前调用。
// allowed=false 表示超额；err 非空表示 Redis 出错。两种情况都不能再调微信。
func (q *MiniappQuota) AllowRequest(ctx context.Context, clientIP, appID string) (allowed bool, err error) {
	n, err := takeMiniappQuotaScript.Run(ctx, q.rdb,
		[]string{miniappQuotaKeyPrefix + "ip:" + clientIP, miniappQuotaKeyPrefix + "app:" + appID},
		MiniappQuotaIPLimit, MiniappQuotaIPWindow.Milliseconds(),
		MiniappQuotaAppLimit, MiniappQuotaAppWindow.Milliseconds(),
	).Int64()
	if err != nil {
		q.log.Errorf(ctx, "miniapp request quota check failed: %v", err)
		return false, err
	}
	return n == 1, nil
}

// AllowPhoneExchange 第二阶段：按 openid 扣减换手机号的次数，取 access_token 和调换号接口之前调用。
func (q *MiniappQuota) AllowPhoneExchange(ctx context.Context, openid string) (allowed bool, err error) {
	n, err := takeMiniappQuotaScript.Run(ctx, q.rdb,
		[]string{miniappQuotaKeyPrefix + "phone:" + openid},
		MiniappQuotaPhoneLimit, MiniappQuotaPhoneWindow.Milliseconds(),
	).Int64()
	if err != nil {
		q.log.Errorf(ctx, "miniapp phone quota check failed: %v", err)
		return false, err
	}
	return n == 1, nil
}
