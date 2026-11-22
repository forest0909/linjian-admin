package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 正式 Make 打包入口拒绝 miniapp_mock 编译标签（A345 审查第 8 条）。
// 标签从 make 的 GOFLAGS 变量或环境变量 GOFLAGS 进来都要拒绝；不带标签时照常放行。
// 用 make -n（只展开配方、不真的编译）：拒绝发生在配方展开时，-n 也会触发。
func TestReleaseMakeTargetsRejectMockTag(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not found")
	}
	const serviceDir = "../.." // app/admin/service，Makefile 在这里

	run := func(env []string, args ...string) (string, error) {
		cmd := exec.Command("make", append([]string{"-n", "-C", serviceDir}, args...)...)
		// 去掉测试进程自己可能带着的 GOFLAGS、MAKEFLAGS，再加上用例给的。
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "GOFLAGS=") && !strings.HasPrefix(kv, "MAKEFLAGS=") && !strings.HasPrefix(kv, "MFLAGS=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, target := range []string{"build_only", "build"} {
		out, err := run(nil, target)
		if err != nil {
			t.Fatalf("%s 不带标签应该放行: %v\n%s", target, err, out)
		}

		cases := map[string]struct {
			env  []string
			args []string
		}{
			"make 变量": {args: []string{"GOFLAGS=-tags=miniapp_mock"}},
			"环境变量":    {env: []string{"GOFLAGS=-tags=miniapp_mock"}},
			"环境变量里和别的标签写在一起": {env: []string{"GOFLAGS=-tags=foo,miniapp_mock"}},
		}
		for name, c := range cases {
			out, err = run(c.env, append([]string{target}, c.args...)...)
			if err == nil {
				t.Fatalf("%s（%s）带 miniapp_mock 标签应该被拒绝，输出:\n%s", target, name, out)
			}
			if !strings.Contains(out, "miniapp_mock build tag is active") {
				t.Fatalf("%s（%s）拒绝的原因不对:\n%s", target, name, out)
			}
			if strings.Contains(out, "go build") {
				t.Fatalf("%s（%s）被拒绝时不应该走到 go build:\n%s", target, name, out)
			}
		}
	}

	// 开发目标不受影响：显式带标签的路径保留。
	out, err := run(nil, "run-miniapp-mock")
	if err != nil || !strings.Contains(out, "-tags miniapp_mock") {
		t.Fatalf("run-miniapp-mock 应该照常展开: %v\n%s", err, out)
	}
}
