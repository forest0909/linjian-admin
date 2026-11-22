package data

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newMiniappQuotaForTest(t *testing.T) (*MiniappQuota, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewMiniappQuotaForTest(rdb), mr
}

// 额度按请求计数，全是成功的请求也会被拦住；窗口过了恢复。
func TestMiniappQuota_RequestLimits(t *testing.T) {
	q, mr := newMiniappQuotaForTest(t)
	ctx := context.Background()

	for i := 0; i < MiniappQuotaIPLimit; i++ {
		ok, err := q.AllowRequest(ctx, "10.0.0.1", "wx1")
		require.NoError(t, err)
		require.True(t, ok, "第 %d 次", i+1)
	}
	ok, err := q.AllowRequest(ctx, "10.0.0.1", "wx1")
	require.NoError(t, err)
	require.False(t, ok, "同一 IP 第 61 次应被拦")

	// 被拦的请求不占 AppID 总量：两个计数器要么都扣，要么都不扣。
	require.Equal(t, "60", mustGet(t, mr, "gowind:miniapp:quota:app:wx1"))

	// 别的 IP 不受影响。
	ok, err = q.AllowRequest(ctx, "10.0.0.2", "wx1")
	require.NoError(t, err)
	require.True(t, ok)

	// 窗口是固定的：后面的请求不会把它往后推。
	require.Equal(t, MiniappQuotaIPWindow, mr.TTL("gowind:miniapp:quota:ip:10.0.0.1"))
	mr.FastForward(MiniappQuotaIPWindow + time.Second)
	ok, err = q.AllowRequest(ctx, "10.0.0.1", "wx1")
	require.NoError(t, err)
	require.True(t, ok)
}

// AppID 总量：不同 IP 加起来每分钟 600 次。
func TestMiniappQuota_AppTotal(t *testing.T) {
	q, _ := newMiniappQuotaForTest(t)
	ctx := context.Background()

	ips := MiniappQuotaAppLimit / MiniappQuotaIPLimit
	for ip := 0; ip < ips; ip++ {
		for i := 0; i < MiniappQuotaIPLimit; i++ {
			ok, err := q.AllowRequest(ctx, "10.1.0."+string(rune('0'+ip)), "wx1")
			require.NoError(t, err)
			require.True(t, ok)
		}
	}
	ok, err := q.AllowRequest(ctx, "10.9.9.9", "wx1")
	require.NoError(t, err)
	require.False(t, ok, "AppID 总量用完，新 IP 也被拦")

	// 换一个 AppID 是另一份总量。
	ok, err = q.AllowRequest(ctx, "10.9.9.9", "wx2")
	require.NoError(t, err)
	require.True(t, ok)
}

// 换手机号按 openid 计数：每 10 分钟 5 次。
func TestMiniappQuota_PhoneExchange(t *testing.T) {
	q, mr := newMiniappQuotaForTest(t)
	ctx := context.Background()

	for i := 0; i < MiniappQuotaPhoneLimit; i++ {
		ok, err := q.AllowPhoneExchange(ctx, "openid-a")
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := q.AllowPhoneExchange(ctx, "openid-a")
	require.NoError(t, err)
	require.False(t, ok)

	ok, err = q.AllowPhoneExchange(ctx, "openid-b")
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(MiniappQuotaPhoneWindow + time.Second)
	ok, err = q.AllowPhoneExchange(ctx, "openid-a")
	require.NoError(t, err)
	require.True(t, ok)
}

// 并发请求不超发：检查和扣减在一段脚本里。
func TestMiniappQuota_ConcurrentRequestsDoNotOversell(t *testing.T) {
	q, _ := newMiniappQuotaForTest(t)
	ctx := context.Background()

	const attempts = MiniappQuotaIPLimit * 3
	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := q.AllowRequest(ctx, "10.0.0.7", "wx1")
			if err == nil && ok {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int64(MiniappQuotaIPLimit), allowed.Load())
}

// Redis 出错：返回错误，调用方不能当成放行。
func TestMiniappQuota_RedisError(t *testing.T) {
	q, mr := newMiniappQuotaForTest(t)
	mr.Close()

	ok, err := q.AllowRequest(context.Background(), "10.0.0.1", "wx1")
	require.Error(t, err)
	require.False(t, ok)
	ok, err = q.AllowPhoneExchange(context.Background(), "openid-a")
	require.Error(t, err)
	require.False(t, ok)
}

func mustGet(t *testing.T, mr *miniredis.Miniredis, key string) string {
	t.Helper()
	v, err := mr.Get(key)
	require.NoError(t, err)
	return v
}
