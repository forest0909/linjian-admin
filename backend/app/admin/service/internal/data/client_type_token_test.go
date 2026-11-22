package data

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"go-wind-admin/third_party/go-utils/trans"
	authnEngine "go-wind-admin/third_party/kratos-authn/engine"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

// signRawAccessToken 用测试密钥直接签一个 access token 并写进 Redis 的 at:{ct}:{uid}:{jti}。
// ct 传 nil 表示不写这个声明（升级前的令牌格式）。
func signRawAccessToken(t *testing.T, a *Authenticator, cache *UserTokenCache, redisType authenticationV1.ClientType, jti string, ct interface{}) string {
	t.Helper()
	claims := authnEngine.AuthClaims{
		"sub": "race",
		"uid": raceTestUID,
		"tid": uint32(0),
		"jti": jti,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	if ct != nil {
		claims["ct"] = ct
	}
	token, err := a.AdminAuthenticator.CreateIdentity(claims)
	require.NoError(t, err)
	require.NoError(t, cache.AddAccessToken(context.Background(), redisType, raceTestUID, jti, token, time.Hour))
	return token
}

func authenticateAccess(a *Authenticator, token string, reqType authenticationV1.ClientType) (*authenticationV1.ValidateTokenResponse, error) {
	return a.Authenticate(context.Background(), &authenticationV1.ValidateTokenRequest{
		Token: token, ClientType: reqType, TokenCategory: authenticationV1.TokenCategory_ACCESS,
	})
}

// JWT 里没有 ct、ct 为 admin、ct 为 app、ct 非法，四种情况。
func TestAuthenticate_ClientTypeClaim(t *testing.T) {
	a, cache := newRaceAuthenticator(t)
	admin, app := authenticationV1.ClientType_admin, authenticationV1.ClientType_app

	t.Run("没有 ct 按 admin", func(t *testing.T) {
		token := signRawAccessToken(t, a, cache, admin, "legacy-jti", nil)
		resp, err := authenticateAccess(a, token, admin)
		require.NoError(t, err)
		require.True(t, resp.GetIsValid())
		require.NotNil(t, resp.GetPayload().ClientType)
		require.Equal(t, admin, resp.GetPayload().GetClientType())
	})

	t.Run("没有 ct 的令牌只在 app 键下有记录时不通过", func(t *testing.T) {
		token := signRawAccessToken(t, a, cache, app, "legacy-in-app-key", nil)
		_, err := authenticateAccess(a, token, admin)
		require.True(t, authenticationV1.IsUnauthorized(err), "got %v", err)
	})

	t.Run("ct 为 admin", func(t *testing.T) {
		token := signRawAccessToken(t, a, cache, admin, "admin-jti", "admin")
		resp, err := authenticateAccess(a, token, admin)
		require.NoError(t, err)
		require.Equal(t, admin, resp.GetPayload().GetClientType())
	})

	t.Run("ct 为 app 按令牌自己的类型查 Redis", func(t *testing.T) {
		token := signRawAccessToken(t, a, cache, app, "app-jti", "app")
		// 请求里的类型写 admin 也不影响：以令牌的 ct 为准。
		resp, err := authenticateAccess(a, token, admin)
		require.NoError(t, err)
		require.True(t, resp.GetIsValid())
		require.Equal(t, app, resp.GetPayload().GetClientType())
	})

	t.Run("ct 为 app 但记录在 admin 键下不通过", func(t *testing.T) {
		token := signRawAccessToken(t, a, cache, admin, "app-in-admin-key", "app")
		_, err := authenticateAccess(a, token, admin)
		require.True(t, authenticationV1.IsUnauthorized(err), "got %v", err)
	})

	for name, bad := range map[string]interface{}{"未知名字": "web", "数字": 1, "空串": ""} {
		t.Run("ct 非法："+name, func(t *testing.T) {
			jti := "bad-" + name
			token := signRawAccessToken(t, a, cache, admin, jti, bad)
			// 两个类型的键下都放上记录，确认拒绝来自 ct 本身。
			require.NoError(t, cache.AddAccessToken(context.Background(), app, raceTestUID, jti, token, time.Hour))
			resp, err := authenticateAccess(a, token, admin)
			require.True(t, authenticationV1.IsUnauthorized(err), "got %v", err)
			require.False(t, resp.GetIsValid())
			require.Nil(t, resp.GetPayload())
		})
	}
}

// app 签发：不再空指针；令牌带 ct=app，记录写在 app 的键下，和 admin 会话互不影响。
func TestCreateUserToken_App(t *testing.T) {
	a, cache := newRaceAuthenticator(t)
	ctx := context.Background()
	admin, app := authenticationV1.ClientType_admin, authenticationV1.ClientType_app

	adminAT, _, _ := raceLogin(t, a)

	p := &authenticationV1.UserTokenPayload{UserId: raceTestUID, Username: trans.Ptr("race")}
	appAT, _, err := a.CreateUserToken(ctx, app, p)
	require.NoError(t, err)
	require.Equal(t, app, p.GetClientType())

	ok, err := cache.IsValidAccessToken(ctx, app, raceTestUID, p.GetJti(), appAT)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.IsValidAccessToken(ctx, admin, raceTestUID, p.GetJti(), appAT)
	require.NoError(t, err)
	require.False(t, ok, "app 令牌不该出现在 admin 的键下")

	resp, err := authenticateAccess(a, appAT, admin)
	require.NoError(t, err)
	require.Equal(t, app, resp.GetPayload().GetClientType())

	resp, err = authenticateAccess(a, adminAT, admin)
	require.NoError(t, err)
	require.Equal(t, admin, resp.GetPayload().GetClientType())

	// 只吊销 app：admin 会话还在。
	require.NoError(t, a.RevokeUserToken(ctx, app, raceTestUID))
	_, err = authenticateAccess(a, appAT, admin)
	require.True(t, authenticationV1.IsUnauthorized(err))
	require.True(t, accessValid(t, a, adminAT))

	// app 的 access token 不能当 refresh token 用。
	appAT2, _, err := a.CreateUserToken(ctx, app, &authenticationV1.UserTokenPayload{UserId: raceTestUID})
	require.NoError(t, err)
	_, _, _, _, err = a.VerifyRefreshToken(ctx, admin, appAT2)
	require.Error(t, err)
}

// failCommands 让指定的 Redis 命令（按名字和首个键的前缀）直接失败。
type failCommands struct {
	match func(name, key string) bool
}

var errInjectedRedis = errors.New("injected redis failure")

func (h *failCommands) DialHook(next redis.DialHook) redis.DialHook {
	return func(c context.Context, n, a string) (net.Conn, error) { return next(c, n, a) }
}
func (h *failCommands) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *failCommands) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		key := ""
		if args := cmd.Args(); len(args) > 1 {
			key = fmt.Sprint(args[1])
		}
		if h.match(strings.ToLower(cmd.Name()), key) {
			cmd.SetErr(errInjectedRedis)
			return errInjectedRedis
		}
		return next(ctx, cmd)
	}
}

// 吊销的每一步出错都要返回给调用方，不能被后面成功的步骤盖掉；其余步骤照常执行。
func TestRevokeToken_ReportsEveryStepError(t *testing.T) {
	admin := authenticationV1.ClientType_admin
	ctx := context.Background()

	t.Run("删除 access token 失败", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		at, _, _ := raceLogin(t, a)
		cache.rdb.AddHook(&failCommands{match: func(name, key string) bool {
			return name == "del" && strings.HasPrefix(key, "at:")
		}})
		err := cache.RevokeToken(ctx, admin, raceTestUID)
		require.ErrorIs(t, err, errInjectedRedis, "前面步骤的错误不能被盖掉")
		// 没删掉的令牌仍被接受：接受条件只看 Redis 记录在不在。
		require.True(t, accessValid(t, a, at))
		// 其余步骤照常做了：refresh token 已清。
		require.Empty(t, cache.GetRefreshTokens(ctx, admin, raceTestUID))
	})

	t.Run("推进撤销版本失败", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		at, _, _ := raceLogin(t, a)
		cache.rdb.AddHook(&failCommands{match: func(name, _ string) bool { return name == "incr" }})
		err := cache.RevokeToken(ctx, admin, raceTestUID)
		require.ErrorIs(t, err, errInjectedRedis)
		require.False(t, accessValid(t, a, at), "版本推进失败时仍尽力删除")
	})

	t.Run("都成功返回 nil", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		raceLogin(t, a)
		require.NoError(t, cache.RevokeToken(ctx, admin, raceTestUID))
	})
}

func TestRevokeTokenByJti_ReportsEveryStepError(t *testing.T) {
	admin := authenticationV1.ClientType_admin
	ctx := context.Background()

	t.Run("删除 access token 失败", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		at, _, jti := raceLogin(t, a)
		cache.rdb.AddHook(&failCommands{match: func(name, key string) bool {
			return name == "del" && strings.HasPrefix(key, "at:")
		}})
		err := cache.RevokeTokenByJti(ctx, admin, raceTestUID, jti)
		require.ErrorIs(t, err, errInjectedRedis, "前面步骤的错误不能被盖掉")
		require.True(t, accessValid(t, a, at))
		require.Empty(t, cache.GetRefreshTokens(ctx, admin, raceTestUID), "其余步骤照常执行")
	})

	t.Run("回溯代际链失败", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		_, _, jti := raceLogin(t, a)
		cache.rdb.AddHook(&failCommands{match: func(name, key string) bool { return name == "get" && strings.HasPrefix(key, "lin:") }})
		require.ErrorIs(t, cache.RevokeTokenByJti(ctx, admin, raceTestUID, jti), errInjectedRedis)
	})

	// 祖先代的 AT 删除失败：返回错误，重试入口（代际链、会话元数据）留着；重试后旧令牌失效。
	t.Run("中途失败后重试能删掉祖先代的令牌", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		oldAT, _, oldJti := raceLogin(t, a)
		newAT, _, newJti := raceLogin(t, a)
		require.NoError(t, a.AddTokenLineage(ctx, admin, raceTestUID, newJti, oldJti))
		require.NoError(t, cache.SaveSessionMeta(ctx, admin, raceTestUID, newJti, &SessionMeta{LoginAt: 1}, time.Hour))

		fail := true
		cache.rdb.AddHook(&failCommands{match: func(name, key string) bool {
			return fail && name == "del" && strings.HasSuffix(key, ":"+oldJti) && strings.HasPrefix(key, "at:")
		}})
		require.ErrorIs(t, cache.RevokeTokenByJti(ctx, admin, raceTestUID, newJti), errInjectedRedis)
		require.False(t, accessValid(t, a, newAT), "当前代的令牌已删")
		require.True(t, accessValid(t, a, oldAT), "祖先代的令牌没删掉")
		meta, err := cache.GetSessionMeta(ctx, admin, raceTestUID, newJti)
		require.NoError(t, err)
		require.NotNil(t, meta, "失败时会话元数据留着，自助踢会话还能找到它重试")

		fail = false
		require.NoError(t, cache.RevokeTokenByJti(ctx, admin, raceTestUID, newJti))
		require.False(t, accessValid(t, a, oldAT), "重试后祖先代的令牌失效")
		meta, err = cache.GetSessionMeta(ctx, admin, raceTestUID, newJti)
		require.NoError(t, err)
		require.Nil(t, meta)
		// 链节点也清了。
		require.False(t, cache.exists(ctx, cache.makeTokenLineageKey(admin, raceTestUID, newJti)))
	})

	t.Run("没有代际链也算成功", func(t *testing.T) {
		a, cache := newRaceAuthenticator(t)
		_, _, jti := raceLogin(t, a)
		require.NoError(t, cache.RevokeTokenByJti(ctx, admin, raceTestUID, jti))
	})
}

// Redis 读不出来不是令牌失效：返回 503，不是 401。
func TestAuthenticate_TokenStoreFailureIsNotUnauthorized(t *testing.T) {
	a, cache := newRaceAuthenticator(t)
	at, _, _ := raceLogin(t, a)
	cache.rdb.AddHook(&failCommands{match: func(name, key string) bool { return name == "get" && strings.HasPrefix(key, "at:") }})
	resp, err := authenticateAccess(a, at, authenticationV1.ClientType_admin)
	require.True(t, authenticationV1.IsServiceUnavailable(err), "got %v", err)
	require.False(t, resp.GetIsValid())
}
