// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package analyzer

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
)

// ========== D3：repo 模式类型信息加载器 ==========
//
// 目标：让 RES/ERR 规则在 repo 模式下"从猜变知道"——
//   - ERR：`_` 丢弃位置的返回值是不是 error 类型（三返回值首丢弃等误报的根治手段）
//   - RES：打开调用首个返回值是否实现 io.Closer
//
// 实现纪律（fail-open）：
//   - 纯标准库实现（go/parser + go/types + source importer），零新依赖；
//   - 任何解析/类型检查失败都不阻塞审查——返回 nil 或查找 miss，
//     规则退回既有词法行为；
//   - source importer 可解析 GOROOT（stdlib 导入），外部模块导入失败时
//     对应标识符类型为空 → 按未知处理；
//   - 仅 repo 模式可用（diff/API 上传没有磁盘上的完整包）。

// RepoTypes 一次 repo 审查的类型信息快照。
type RepoTypes struct {
	repoPath string
	fset     *token.FileSet
	files    map[string]*ast.File   // 绝对路径 → AST
	infos    map[string]*types.Info // 绝对路径 → 所在包的类型信息
}

var closerIface = types.NewInterfaceType([]*types.Func{
	types.NewFunc(token.NoPos, nil, "Close", types.NewSignature(nil,
		nil, types.NewTuple(
			types.NewVar(0, nil, "", types.Universe.Lookup("error").Type())), false)),
}, nil).Complete()

var errIface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// LoadRepoTypes 加载 repo 内指定文件（相对路径）所在包的类型信息。
// 任何失败返回 nil——调用方与规则一律按"无类型信息"处理。
func LoadRepoTypes(repoPath string, relFilePaths []string) *RepoTypes {
	rt := &RepoTypes{
		repoPath: repoPath,
		fset:     token.NewFileSet(),
		files:    map[string]*ast.File{},
		infos:    map[string]*types.Info{},
	}

	// 按目录分组（同目录 = 同包，一次类型检查）
	dirFiles := map[string][]string{}
	for _, rel := range relFilePaths {
		rel = filepath.ToSlash(filepath.Clean(rel))
		abs := filepath.Join(repoPath, rel)
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		dir := filepath.Dir(abs)
		dirFiles[dir] = append(dirFiles[dir], abs)
	}

	for dir, absPaths := range dirFiles {
		rt.loadPackageDir(dir, absPaths)
	}
	if len(rt.files) == 0 {
		return nil
	}
	return rt
}

// loadPackageDir 解析并类型检查一个包目录（失败静默降级）。
func (rt *RepoTypes) loadPackageDir(dir string, absPaths []string) {
	var files []*ast.File
	for _, abs := range absPaths {
		f, err := parser.ParseFile(rt.fset, abs, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return
	}

	// 补齐同包其余 .go 文件（类型检查需要完整包视图）
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		if rt.files[abs] != nil {
			continue
		}
		f, err := parser.ParseFile(rt.fset, abs, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		files = append(files, f)
	}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	conf := types.Config{
		Importer: importer.ForCompiler(rt.fset, "source", nil),
		// 类型错误不中断——拿到部分类型信息好过没有
		Error: func(err error) {},
	}
	pkgPath := files[0].Name.Name
	_, _ = conf.Check(pkgPath, rt.fset, files, info)

	for _, f := range files {
		abs := rt.fset.Position(f.Pos()).Filename
		rt.files[abs] = f
		rt.infos[abs] = info
	}
}

// assignmentAt 定位 file:line 上的赋值语句（返回语句的 LHS 表达式与 RHS）。
// 返回 nil 表示该行没有可识别的赋值。
func (rt *RepoTypes) assignmentAt(absFile string, line int) *ast.AssignStmt {
	f := rt.files[absFile]
	info := rt.infos[absFile]
	if f == nil || info == nil {
		return nil
	}
	var found *ast.AssignStmt
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if as, ok := n.(*ast.AssignStmt); ok {
			if rt.fset.Position(as.Pos()).Line == line {
				found = as
				return false
			}
		}
		return true
	})
	return found
}

// rhsTuple 提取赋值右侧调用的返回值元组（右侧非调用或无类型信息返回 nil）。
func (rt *RepoTypes) rhsTuple(absFile string, as *ast.AssignStmt) *types.Tuple {
	info := rt.infos[absFile]
	if info == nil {
		return nil
	}
	for _, r := range as.Rhs {
		if call, ok := r.(*ast.CallExpr); ok {
			if tv, ok := info.Types[call]; ok {
				if t, ok := tv.Type.(*types.Tuple); ok {
					return t
				}
			}
		}
	}
	return nil
}

// LHSPositionType 返回 file:line（新文件行号）赋值语句第 idx 个 LHS 的类型。
// idx 越界/类型未知/无信息 → nil（调用方按未知处理）。
func (rt *RepoTypes) LHSPositionType(relFile string, line, idx int) types.Type {
	abs := filepath.Join(rt.repoPath, filepath.FromSlash(relFile))
	as := rt.assignmentAt(abs, line)
	if as == nil || idx >= len(as.Lhs) {
		return nil
	}
	if tuple := rt.rhsTuple(abs, as); tuple != nil && idx < tuple.Len() {
		return tuple.At(idx).Type()
	}
	// 单返回值调用：RHS 调用的类型就是返回类型（无元组包装）
	if idx == 0 {
		for _, r := range as.Rhs {
			if call, ok := r.(*ast.CallExpr); ok {
				if tv, ok := rt.infos[abs].Types[call]; ok && tv.Type != nil {
					return tv.Type
				}
			}
		}
	}
	// 非调用 RHS（索引表达式/类型断言等）：直接查 LHS 类型
	if tv, ok := rt.infos[abs].Types[as.Lhs[idx]]; ok {
		return tv.Type
	}
	return nil
}

// FirstLHSImplementsCloser 判断 file:line 赋值第一个 LHS 是否实现 io.Closer。
// 返回 (是否实现, 类型是否已知)；未知时规则退回词法行为。
func (rt *RepoTypes) FirstLHSImplementsCloser(relFile string, line int) (implCloser, known bool) {
	t := rt.LHSPositionType(relFile, line, 0)
	if t == nil {
		return false, false
	}
	return types.Implements(t, closerIface), true
}

// IsErrorType 判断类型是否为 error（或实现 error 接口）。
func IsErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	return types.Implements(t, errIface)
}
