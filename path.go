package touka

import (
	"path"
	"strings"
)

// resolveRoutePath 安全地拼接基础路径和相对路径，并正确处理尾部斜杠。
// 这是一个为高性能路由注册优化的版本。
func resolveRoutePath(basePath, relativePath string) string {
	// 如果相对路径为空，直接返回基础路径
	if relativePath == "" {
		return basePath
	}
	// 如果基础路径为空，直接返回相对路径（确保以/开头）
	if basePath == "" {
		return relativePath
	}

	// 使用 strings.Builder 来高效构建路径，避免多次字符串分配
	var b strings.Builder
	// 估算一个合理的容量以减少扩容
	b.Grow(len(basePath) + len(relativePath) + 1)
	b.WriteString(basePath)

	// 检查 basePath 是否以斜杠结尾
	if basePath[len(basePath)-1] != '/' {
		b.WriteByte('/') // 如果没有，则添加
	}

	// 检查 relativePath 是否以斜杠开头，如果是，则移除
	if relativePath[0] == '/' {
		b.WriteString(relativePath[1:])
	} else {
		b.WriteString(relativePath)
	}

	// path.Clean 仍然是处理 '..' 和 '//' 等复杂情况最可靠的方式。
	// 我们可以只在最终结果上调用一次，而不是在拼接过程中。
	finalPath := path.Clean(b.String())

	// 关键：如果原始 relativePath 有尾部斜杠，但 Clean 把它移除了，我们要加回来。
	// 只有当最终路径不是根路径 "/" 时才需要加回。
	if strings.HasSuffix(relativePath, "/") && finalPath != "/" {
		return finalPath + "/"
	}

	return finalPath
}
