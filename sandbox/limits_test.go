// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// M3 沙箱限制测试集：超时 / 输出截断 / env 白名单 / 网络隔离 / 只读 rootfs / 非 root。
// 容器类用例在 Docker 不可用的环境自动 SKIP（退出标准中 Docker 全链路需有 Docker 的环境验证）。

func skipWithoutDocker(t *testing.T) {
	t.Helper()
	if err := checkDockerAvailable(); err != nil {
		t.Skipf("Docker 不可用，跳过容器限制测试: %v", err)
	}
}

// TestLimits_Local_Timeout 超时限制：命令被终止并标记 TimedOut。
func TestLimits_Local_Timeout(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   "sleep 10",
		Timeout:   500 * time.Millisecond,
		MaxOutput: 1024,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if !res.TimedOut {
		t.Error("TimedOut 应为 true")
	}
	if res.ExitCode != -1 {
		t.Errorf("超时 ExitCode 应为 -1, 得到 %d", res.ExitCode)
	}
}

// TestLimits_Local_OutputTruncation 输出截断：超限输出被截断并标记。
func TestLimits_Local_OutputTruncation(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   "for i in $(seq 1 500); do echo \"line-$i-aaaaaaaaaaaaaaaa\"; done",
		Timeout:   10 * time.Second,
		MaxOutput: 300,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if !res.Truncated {
		t.Error("Truncated 应为 true")
	}
	if len(res.Output) > 400 {
		t.Errorf("输出应被截断到 ~300B, 实际 %d", len(res.Output))
	}
}

// TestLimits_Local_EnvPassthrough local 后端 env 直传（白名单由容器后端执行）；
// 输出中的密钥样式值由统一脱敏兜底。
func TestLimits_Local_EnvPassthrough(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   `echo "SECRET=$CRA_SECRET"`,
		Env:       map[string]string{"CRA_SECRET": "topsecret-2026"},
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if strings.Contains(res.Output, "topsecret-2026") {
		t.Errorf("输出泄漏明文: %q", res.Output)
	}
}

// TestLimits_Container_EnvWhitelist 手写容器后端的 env 白名单：
// 非白名单变量不得进入容器环境。
func TestLimits_Container_EnvWhitelist(t *testing.T) {
	skipWithoutDocker(t)
	sb, err := NewContainerSandbox("")
	if err != nil {
		t.Fatalf("创建容器沙箱失败: %v", err)
	}
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   `sh -c 'echo "SECRET=${CRA_SECRET:-EMPTY}"'`,
		Env:       map[string]string{"CRA_SECRET": "topsecret-2026", "PATH": "/usr/bin:/bin"},
		Timeout:   30 * time.Second,
		MaxOutput: 1024,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if strings.Contains(res.Output, "topsecret-2026") {
		t.Errorf("非白名单 env 泄漏进容器: %q", res.Output)
	}
	if !strings.Contains(res.Output, "EMPTY") {
		t.Errorf("白名单外的变量应不可见（期望 EMPTY）, 得到 %q", res.Output)
	}
}

// TestLimits_ContainerFX_NetworkIsolation 框架容器后端网络隔离：
// NetworkMode=none 下外连必须失败。
func TestLimits_ContainerFX_NetworkIsolation(t *testing.T) {
	skipWithoutDocker(t)
	sb, err := NewFrameworkContainerSandbox("")
	if err != nil {
		t.Fatalf("创建框架容器沙箱失败: %v", err)
	}
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   "wget -T 3 -qO- http://example.com >/dev/null 2>&1; echo \"exit=$?\"",
		Timeout:   30 * time.Second,
		MaxOutput: 1024,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if strings.Contains(res.Output, "exit=0") {
		t.Errorf("网络隔离失效——外连成功: %q", res.Output)
	}
}

// TestLimits_ContainerFX_ReadOnlyRootfs 框架容器后端只读 rootfs：
// 系统目录不可写。
func TestLimits_ContainerFX_ReadOnlyRootfs(t *testing.T) {
	skipWithoutDocker(t)
	sb, err := NewFrameworkContainerSandbox("")
	if err != nil {
		t.Fatalf("创建框架容器沙箱失败: %v", err)
	}
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   `touch /etc/cra-should-fail 2>/dev/null && echo "WROTE" || echo "DENIED"`,
		Timeout:   30 * time.Second,
		MaxOutput: 1024,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if strings.Contains(res.Output, "WROTE") {
		t.Error("只读 rootfs 失效——/etc 可写")
	}
}

// TestLimits_ContainerFX_NonRoot 框架容器后端非 root：uid 非 0。
func TestLimits_ContainerFX_NonRoot(t *testing.T) {
	skipWithoutDocker(t)
	sb, err := NewFrameworkContainerSandbox("")
	if err != nil {
		t.Fatalf("创建框架容器沙箱失败: %v", err)
	}
	defer sb.Close()

	res, err := sb.Execute(context.Background(), ExecuteOptions{
		Command:   "id -u",
		Timeout:   30 * time.Second,
		MaxOutput: 1024,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	uid := strings.TrimSpace(res.Output)
	if uid == "" || uid == "0" {
		t.Errorf("容器内应为非 root 用户, 得到 uid=%q", uid)
	}
}

// TestLimits_E2B_NoKeyGraceful E2B 后端无 key 时优雅失败（不 panic、错误信息明确）。
func TestLimits_E2B_NoKeyGraceful(t *testing.T) {
	t.Setenv("E2B_API_KEY", "")
	_, err := NewE2BSandbox()
	if err == nil {
		t.Fatal("无 E2B_API_KEY 应返回错误")
	}
	if !strings.Contains(err.Error(), "E2B_API_KEY") {
		t.Errorf("错误应指明缺失的 key, 得到 %v", err)
	}
}
