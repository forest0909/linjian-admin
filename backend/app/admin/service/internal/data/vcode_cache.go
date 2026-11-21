package data

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	"go-wind-admin/third_party/kratos-bootstrap/logger"

	"go-wind-admin/pkg/serviceid"
)

// vcodeKeyPrefix 验证码键前缀（与 captcha 同前缀域便于统一清理）
const vcodeKeyPrefix = serviceid.ProjectName + ":vcode:"

// VCodeCache 一次性验证码缓存（Redis）。
// purpose 区分业务场景（reset_password / bind_contact），
// 校验成功即删除（单次有效），TTL 到期自动清理。
type VCodeCache struct {
	rdb *redis.Client
	log *logger.Helper
}

func NewVCodeCache(ctx *bLogger.Context, rdb *redis.Client) *VCodeCache {
	return &VCodeCache{
		rdb: rdb,
		log: ctx.NewLoggerHelper("vcode/cache"),
	}
}

// Save 保存验证码，ttl 到期自动删除。
func (c *VCodeCache) Save(purpose, identifier, code string, ttl time.Duration) error {
	key := vcodeKeyPrefix + purpose + ":" + identifier
	if err := c.rdb.Set(context.Background(), key, code, ttl).Err(); err != nil {
		c.log.Errorf(context.Background(), "save vcode failed: %v", err)
		return err
	}
	return nil
}

// Verify 校验验证码（消费型：匹配即删除，单次有效）。
func (c *VCodeCache) Verify(purpose, identifier, code string) bool {
	key := vcodeKeyPrefix + purpose + ":" + identifier
	v, err := c.rdb.Get(context.Background(), key).Result()
	if err != nil || v != code {
		return false
	}
	c.rdb.Del(context.Background(), key)
	return true
}

// ---- 归属到用户的验证码（改手机号、改邮箱）----
//
// 键包含用户 ID：别的用户申请的验证码不能用。校验、消费、计错在一段脚本里原子完成。

const (
	// VCodeMaxAttempts 同一个验证码最多错几次，到数即作废。
	VCodeMaxAttempts = 5

	// 发送额度：同一用户每小时 5 次，同一联系方式每分钟 1 次。重新发送不会清掉额度。
	vcodeSendUserLimit     = 5
	vcodeSendUserWindow    = time.Hour
	vcodeSendContactLimit  = 1
	vcodeSendContactWindow = time.Minute
)

// verifyUserVCodeScript 原子校验。KEYS: [codeKey]，ARGV: [code, maxAttempts]。
// 返回 1=正确（已删除，单次有效），0=不存在、已过期或不正确（不正确时计一次，到上限删除）。
var verifyUserVCodeScript = redis.NewScript(`
	local stored = redis.call('HGET', KEYS[1], 'code')
	if not stored then
		return 0
	end
	if stored == ARGV[1] then
		redis.call('DEL', KEYS[1])
		return 1
	end
	local fails = redis.call('HINCRBY', KEYS[1], 'fails', 1)
	if fails >= tonumber(ARGV[2]) then
		redis.call('DEL', KEYS[1])
	end
	return 0
`)

// allowVCodeSendScript 原子检查并扣减两个发送额度，任何一个用完都不扣。
// KEYS: [userKey, contactKey]，ARGV: [userLimit, userWindowMs, contactLimit, contactWindowMs]。返回 1=放行，0=超额。
var allowVCodeSendScript = redis.NewScript(`
	local u = tonumber(redis.call('GET', KEYS[1]) or '0')
	local c = tonumber(redis.call('GET', KEYS[2]) or '0')
	if u >= tonumber(ARGV[1]) or c >= tonumber(ARGV[3]) then
		return 0
	end
	if redis.call('INCR', KEYS[1]) == 1 then
		redis.call('PEXPIRE', KEYS[1], ARGV[2])
	end
	if redis.call('INCR', KEYS[2]) == 1 then
		redis.call('PEXPIRE', KEYS[2], ARGV[4])
	end
	return 1
`)

func userVCodeKey(userID uint32, purpose, contact string) string {
	return vcodeKeyPrefix + purpose + ":" + strconv.FormatUint(uint64(userID), 10) + ":" + contact
}

// AllowSendForUser 发送前扣减额度。allowed=false 表示超额；err 非空表示 Redis 出错，调用方应拒绝发送。
func (c *VCodeCache) AllowSendForUser(ctx context.Context, userID uint32, contact string) (allowed bool, err error) {
	n, err := allowVCodeSendScript.Run(ctx, c.rdb,
		[]string{
			vcodeKeyPrefix + "quota:user:" + strconv.FormatUint(uint64(userID), 10),
			vcodeKeyPrefix + "quota:contact:" + contact,
		},
		vcodeSendUserLimit, vcodeSendUserWindow.Milliseconds(),
		vcodeSendContactLimit, vcodeSendContactWindow.Milliseconds(),
	).Int64()
	if err != nil {
		c.log.Errorf(ctx, "vcode send quota check failed for user [%d]: %v", userID, err)
		return false, err
	}
	return n == 1, nil
}

// SaveForUser 保存某个用户为某个联系方式申请的验证码，覆盖上一枚并把错误计数清零。
func (c *VCodeCache) SaveForUser(ctx context.Context, userID uint32, purpose, contact, code string, ttl time.Duration) error {
	key := userVCodeKey(userID, purpose, contact)
	pipe := c.rdb.TxPipeline()
	pipe.Del(ctx, key)
	pipe.HSet(ctx, key, "code", code, "fails", 0)
	pipe.PExpire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		c.log.Errorf(ctx, "save vcode failed for user [%d]: %v", userID, err)
		return err
	}
	return nil
}

// VerifyForUser 校验并消费验证码。ok=false 表示不存在、已过期、不正确或错误次数已到上限；
// err 非空表示 Redis 出错，调用方必须拒绝，不能放行。
func (c *VCodeCache) VerifyForUser(ctx context.Context, userID uint32, purpose, contact, code string) (ok bool, err error) {
	n, err := verifyUserVCodeScript.Run(ctx, c.rdb,
		[]string{userVCodeKey(userID, purpose, contact)},
		code, VCodeMaxAttempts,
	).Int64()
	if err != nil {
		c.log.Errorf(ctx, "verify vcode failed for user [%d]: %v", userID, err)
		return false, err
	}
	return n == 1, nil
}
