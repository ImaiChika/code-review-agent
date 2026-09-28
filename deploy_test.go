// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// M7-F7：部署资产一致性测试。
// Docker 实机验证在 CI（server-image job，GitHub Actions runner 自带 Docker，
// 本机 daemon 不一定可用）；这里锁死部署文件之间的静态契约，
// 防止"改了 serve 参数忘了改 compose"这类漂移。
package main

import (
	"os"
	"strings"
	"testing"
)

func readDeployFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(data)
}

func TestDeployAssets(t *testing.T) {
	compose := readDeployFile(t, "docker-compose.yml")

	checks := []struct {
		name string
		file string
		need string
	}{
		{"多阶段构建存在", "Dockerfile.server", "FROM golang:"},
		{"运行阶段 alpine", "Dockerfile.server", "FROM alpine:"},
		{"CGO 构建（go-sqlite3 依赖）", "Dockerfile.server", "CGO_ENABLED=1"},
		{"数据卷挂点", "Dockerfile.server", "VOLUME"},
		{"默认 serve 端口与 compose 一致", "Dockerfile.server", `["serve", "--port", "8080"`},
		{"compose 引用服务 Dockerfile", "docker-compose.yml", "Dockerfile.server"},
		{"compose 暴露 8080", "docker-compose.yml", "8080:8080"},
		{"compose 数据卷", "docker-compose.yml", "cra-data:/data"},
		{"compose 健康检查走 /api/health", "docker-compose.yml", "/api/health"},
		{"compose 透传 AUTH_TOKEN", "docker-compose.yml", "AUTH_TOKEN"},
		{"compose 透传 ALLOW_REPOS", "docker-compose.yml", "ALLOW_REPOS"},
		{"沙箱镜像与服务镜像分离", "Dockerfile.server", "cr-sandbox"},
	}
	for _, c := range checks {
		if !strings.Contains(readDeployFile(t, c.file), c.need) {
			t.Errorf("%s：%s 应包含 %q", c.name, c.file, c.need)
		}
	}

	// compose 的镜像名/容器名与文档一致
	if !strings.Contains(compose, "code-review-agent:latest") {
		t.Error("compose 应构建 code-review-agent:latest 镜像")
	}

	// README 部署一节存在
	readme := readDeployFile(t, "README.md")
	if !strings.Contains(readme, "docker compose up -d") {
		t.Error("README 应包含 Docker 部署说明")
	}
}

// TestServeEnvFallbackContract M7-F7：serve 支持环境变量注入认证与白名单
// （容器部署形态）。逻辑在 main.go runServe 中，这里锁定契约本身：
// 环境变量名与 compose/README 文档一致，flag 优先级注释在位。
func TestServeEnvFallbackContract(t *testing.T) {
	src := readDeployFile(t, "main.go")
	for _, env := range []string{"AUTH_TOKEN", "ALLOW_REPOS"} {
		if !strings.Contains(src, `os.Getenv("`+env+`")`) {
			t.Errorf("runServe 应支持环境变量 %s 回退（容器部署契约）", env)
		}
	}
}
