// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package server — M7-F3 文件上传输入。
//
// POST /api/reviews/upload（multipart/form-data）：
//   - 字段 files：一个或多个文件（文本），或单个 .zip 压缩包
//   - 解压/读取后全部转成"整体按新增行审查"的输入，走同一条异步审查管线
//
// zip 安全边界：路径穿越（zip-slip）拒绝、条目数 ≤500、单条目解压 ≤2MB、
// 总解压 ≤20MB、二进制文件跳过（与"非 Go 文件走通用规则"的 W1 语义对齐前，
// 先保证只把文本内容交给规则引擎）。
package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"path"
	"strings"

	"code-review-agent/diff"
)

// zip 安全限制。
const (
	maxZipEntries      = 500      // 条目数上限
	maxZipEntrySize    = 2 << 20  // 单条目解压上限 2MB
	maxZipTotalSize    = 20 << 20 // 总解压上限 20MB
	maxUploadFiles     = 200      // 直接上传文件数上限
	maxUploadFileBytes = 10 << 20 // 单文件内容上限（对齐请求体上限以内）
)

// parseUploadFiles 解析 multipart 上传，返回内存文件内容列表。
// form 须已经过 ParseMultipartForm。
func parseUploadFiles(form *multipart.Form) ([]diff.NamedContent, error) {
	if len(form.File) == 0 {
		return nil, fmt.Errorf("没有上传文件（字段名 files）")
	}

	var contents []diff.NamedContent
	total := 0
	for _, headers := range form.File {
		for _, fh := range headers {
			if total >= maxUploadFiles {
				return nil, fmt.Errorf("上传文件数超过上限 %d", maxUploadFiles)
			}
			f, err := fh.Open()
			if err != nil {
				return nil, fmt.Errorf("打开 %s 失败: %w", fh.Filename, err)
			}
			data, err := io.ReadAll(io.LimitReader(f, maxUploadFileBytes+1))
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("读取 %s 失败: %w", fh.Filename, err)
			}
			if int64(len(data)) > maxUploadFileBytes {
				return nil, fmt.Errorf("文件 %s 超过单文件上限 %d MB", fh.Filename, maxUploadFileBytes>>20)
			}

			if strings.HasSuffix(strings.ToLower(fh.Filename), ".zip") {
				unpacked, err := unzipToContents(data)
				if err != nil {
					return nil, err
				}
				contents = append(contents, unpacked...)
				total += len(unpacked)
				continue
			}

			// 普通文件：跳过二进制（含 NUL 字节视为二进制）
			if bytes.IndexByte(data, 0) >= 0 {
				continue
			}
			contents = append(contents, diff.NamedContent{Name: fh.Filename, Content: string(data)})
			total++
		}
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("上传内容为空或全是二进制/压缩目录，没有可审查的文本文件")
	}
	return contents, nil
}

// unzipToContents 解压 zip 到内存，带安全边界。
func unzipToContents(data []byte) ([]diff.NamedContent, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("解析 zip 失败: %w", err)
	}
	if len(zr.File) > maxZipEntries {
		return nil, fmt.Errorf("zip 条目数超过上限 %d", maxZipEntries)
	}

	var contents []diff.NamedContent
	var total int64
	for _, zf := range zr.File {
		name := path.Clean(zf.Name)
		// zip-slip：绝对路径 / 盘符 / 向上跳转的条目直接拒绝
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") ||
			(len(name) > 1 && name[1] == ':') {
			return nil, fmt.Errorf("zip 包含不安全的路径 %q，已拒绝", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("解压 %s 失败: %w", zf.Name, err)
		}
		buf, err := io.ReadAll(io.LimitReader(rc, maxZipEntrySize+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("解压 %s 失败: %w", zf.Name, err)
		}
		if int64(len(buf)) > maxZipEntrySize {
			return nil, fmt.Errorf("zip 内文件 %s 超过单条目上限 %d MB", zf.Name, maxZipEntrySize>>20)
		}
		total += int64(len(buf))
		if total > maxZipTotalSize {
			return nil, fmt.Errorf("zip 总解压大小超过上限 %d MB（疑似压缩炸弹）", maxZipTotalSize>>20)
		}
		if bytes.IndexByte(buf, 0) >= 0 {
			continue // 二进制跳过
		}
		contents = append(contents, diff.NamedContent{Name: name, Content: string(buf)})
	}
	return contents, nil
}
