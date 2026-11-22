//go:build miniapp_mock

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	"go-wind-admin/app/admin/service/internal/service"
)

// 开发构建：mock: true 时装配 mock 客户端，不需要 Secret。
func TestNewMiniappRuntime_MockBuild(t *testing.T) {
	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())

	rt, err := newMiniappRuntime(boot, service.MiniappConfig{AppID: "wx-mock", Mock: true}, nil)
	require.NoError(t, err)
	require.True(t, rt.Enabled())
	openid, err := rt.Client.Code2Session(context.Background(), "mock_openid_dev")
	require.NoError(t, err)
	require.Equal(t, "dev", openid)

	// 没开 mock 时和正式构建一样要求 Secret。
	_, err = newMiniappRuntime(boot, service.MiniappConfig{AppID: "wx1"}, nil)
	require.Error(t, err)
}
