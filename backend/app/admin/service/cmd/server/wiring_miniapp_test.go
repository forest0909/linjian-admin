//go:build !miniapp_mock

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	"go-wind-admin/app/admin/service/internal/service"
)

// 默认构建：mock: true 时启动失败，有没有 Secret 都一样；配了 app_id 没配 Secret 启动失败。
func TestNewMiniappRuntime_DefaultBuild(t *testing.T) {
	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())

	for name, cfg := range map[string]service.MiniappConfig{
		"mock 且没有 Secret":      {AppID: "wx1", Mock: true},
		"mock 且有 Secret":       {AppID: "wx1", AppSecret: "s", Mock: true},
		"mock 且没有 app_id":      {Mock: true},
		"有 app_id 没有 Secret": {AppID: "wx1"},
	} {
		rt, err := newMiniappRuntime(boot, cfg, nil)
		require.Error(t, err, name)
		require.Nil(t, rt, name)
	}

	// app_id 留空：未启用，不报错。
	rt, err := newMiniappRuntime(boot, service.MiniappConfig{}, nil)
	require.NoError(t, err)
	require.False(t, rt.Enabled())

	// 配齐：装配真实客户端。
	rt, err = newMiniappRuntime(boot, service.MiniappConfig{AppID: "wx1", AppSecret: "s"}, nil)
	require.NoError(t, err)
	require.True(t, rt.Enabled())
	require.Equal(t, "wx1", rt.AppID)
}
