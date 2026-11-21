package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	conf "go-wind-admin/third_party/kratos-bootstrap/api/gen/go/conf/v1"
	sseServer "go-wind-admin/third_party/kratos-transport/transport/sse"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	"go-wind-admin/app/admin/service/internal/data"
)

// 已建立的管理端 SSE 连接：只吊销 app 令牌（小程序下线、改号、解绑）之后继续收到消息；
// admin 令牌被全量吊销时断开。
func TestSseRevokeHook_OnlyAdminRevokeClosesStream(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	authenticator, err := data.NewAuthenticatorForTest(
		&conf.Authentication_Jwt{Method: "HS256", Key: "sse-hook-test-hs256-secret-key-0123456789abcdef"},
		data.NewUserTokenCacheForTest(rdb),
	)
	require.NoError(t, err)

	srv := sseServer.NewServer(sseServer.WithAutoStream(true))
	closeSseStreamsOnAdminRevoke(authenticator, srv)

	mux := http.NewServeMux()
	mux.HandleFunc("/events", srv.ServeHTTP)
	hs := httptest.NewServer(mux)

	resp, err := http.Get(hs.URL + "/events?stream=42")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	defer func() {
		_ = resp.Body.Close()
		hs.CloseClientConnections()
		hs.Close()
	}()

	lines := make(chan string, 16)
	closed := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data:") {
				lines <- line
			}
		}
		close(closed)
	}()
	time.Sleep(100 * time.Millisecond)

	expectMessage := func(payload string) {
		t.Helper()
		srv.Publish(context.Background(), "42", &sseServer.Event{Data: []byte(payload)})
		select {
		case line := <-lines:
			require.Contains(t, line, payload)
		case <-closed:
			t.Fatal("连接不该断开")
		case <-time.After(2 * time.Second):
			t.Fatal("没收到消息")
		}
	}

	ctx := context.Background()
	expectMessage("before")

	// 只吊销 app：连接还在，消息照收。
	require.NoError(t, authenticator.RevokeUserToken(ctx, authenticationV1.ClientType_app, 42))
	expectMessage("after-app-revoke")

	// 别的用户的 admin 吊销也不影响。
	require.NoError(t, authenticator.RevokeUserToken(ctx, authenticationV1.ClientType_admin, 43))
	expectMessage("after-other-user-revoke")

	// 本人的 admin 全量吊销：断开。
	require.NoError(t, authenticator.RevokeUserToken(ctx, authenticationV1.ClientType_admin, 42))
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("admin 全量吊销后连接应被服务端关闭")
	}
}
