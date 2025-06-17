// touka/path_test.go
package touka

import (
	"fmt"
	"path"
	"strings"
	"testing"
)

func TestResolveRoutePath(t *testing.T) {
	// 定义一组测试用例
	testCases := []struct {
		basePath     string
		relativePath string
		expected     string
	}{
		// --- 基本情况 ---
		{basePath: "/api", relativePath: "/v1", expected: "/api/v1"},
		{basePath: "/api/", relativePath: "v1", expected: "/api/v1"},
		{basePath: "/api", relativePath: "v1", expected: "/api/v1"},
		{basePath: "/api/", relativePath: "/v1", expected: "/api/v1"},

		// --- 尾部斜杠处理 ---
		{basePath: "/api", relativePath: "/v1/", expected: "/api/v1/"},
		{basePath: "/api/", relativePath: "v1/", expected: "/api/v1/"},
		{basePath: "", relativePath: "/v1/", expected: "/v1/"},
		{basePath: "/", relativePath: "/v1/", expected: "/v1/"},

		// --- 根路径和空路径 ---
		{basePath: "/", relativePath: "/", expected: "/"},
		{basePath: "/api", relativePath: "/", expected: "/api/"},
		{basePath: "/api/", relativePath: "/", expected: "/api/"},
		{basePath: "/", relativePath: "/users", expected: "/users"},
		{basePath: "/users", relativePath: "", expected: "/users"},
		{basePath: "", relativePath: "/users", expected: "/users"},

		// --- 路径清理测试 (由 path.Clean 处理) ---
		{basePath: "/api/v1", relativePath: "../v2", expected: "/api/v2"},
		{basePath: "/api/v1/", relativePath: "../v2/", expected: "/api/v2/"},
		{basePath: "/api//v1", relativePath: "/users", expected: "/api/v1/users"},
		{basePath: "/api/./v1", relativePath: "/users", expected: "/api/v1/users"},
	}

	for _, tc := range testCases {
		// 使用 t.Run 为每个测试用例创建一个子测试，方便定位问题
		testName := fmt.Sprintf("base:'%s', rel:'%s'", tc.basePath, tc.relativePath)
		t.Run(testName, func(t *testing.T) {
			result := resolveRoutePath(tc.basePath, tc.relativePath)
			if result != tc.expected {
				t.Errorf("resolveRoutePath('%s', '%s') = '%s'; want '%s'",
					tc.basePath, tc.relativePath, result, tc.expected)
			}
		})
	}
}

// 性能基准测试，用于观测优化效果
func BenchmarkResolveRoutePath(b *testing.B) {
	basePath := "/api/v1/some/long/path"
	relativePath := "/users/profile/details/"

	// b.N 是由 testing 包提供的循环次数
	for i := 0; i < b.N; i++ {
		// 在循环内调用被测试的函数
		resolveRoutePath(basePath, relativePath)
	}
}

// （可选）可以保留旧的实现，进行性能对比
func resolveRoutePath_Old(basePath, relativePath string) string {
	if relativePath == "/" {
		if basePath != "" && basePath != "/" && !strings.HasSuffix(basePath, "/") {
			return basePath + "/"
		}
		return basePath
	}
	finalPath := path.Clean(basePath + "/" + relativePath)
	if strings.HasSuffix(relativePath, "/") && !strings.HasSuffix(finalPath, "/") {
		return finalPath + "/"
	}
	return finalPath
}

func BenchmarkResolveRoutePath_Old(b *testing.B) {
	basePath := "/api/v1/some/long/path"
	relativePath := "/users/profile/details/"
	for i := 0; i < b.N; i++ {
		resolveRoutePath_Old(basePath, relativePath)
	}
}

func BenchmarkJoinStd(b *testing.B) {
	basePath := "/api/v1/some/long/path"
	relativePath := "/users/profile/details/"
	for i := 0; i < b.N; i++ {
		path.Join(basePath, relativePath)
	}
}
