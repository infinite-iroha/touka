// Copyright 2013 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// at https://github.com/julienschmidt/httprouter/blob/master/LICENSE
// This tree.go is gin's fork, you can see https://github.com/gin-gonic/gin/blob/master/tree.go

package touka // 定义包名为 touka，该包可能是一个路由或Web框架的核心组件

import (
	"bytes"        // 导入 bytes 包，用于操作字节切片
	"net/url"      // 导入 net/url 包，用于 URL 解析和转义
	"strings"      // 导入 strings 包，用于字符串操作
	"unicode"      // 导入 unicode 包，用于处理 Unicode 字符
	"unicode/utf8" // 导入 unicode/utf8 包，用于 UTF-8 编码和解码
	"unsafe"       // 导入 unsafe 包，用于不安全的类型转换，以避免内存分配
)

// StringToBytes 将字符串转换为字节切片，不进行内存分配。
// 更多详情，请参见 https://github.com/golang/go/issues/53003#issuecomment-1140276077。
// 注意：此函数使用 unsafe 包，应谨慎使用，因为它可能导致内存不安全。
func StringToBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// BytesToString 将字节切片转换为字符串，不进行内存分配。
// 更多详情，请参见 https://github.com/golang/go/issues/53003#issuecomment-1140276077。
// 注意：此函数使用 unsafe 包，应谨慎使用，因为它可能导致内存不安全。
func BytesToString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

var (
	strColon = []byte(":") // 定义字节切片常量，表示冒号，用于路径参数识别
	strStar  = []byte("*") // 定义字节切片常量，表示星号，用于捕获所有路径识别
	strSlash = []byte("/") // 定义字节切片常量，表示斜杠，用于路径分隔符识别
)

// Param 是单个 URL 参数，由键和值组成。
type Param struct {
	Key   string // 参数的键名
	Value string // 参数的值
}

// Params 是 Param 类型的切片，由路由器返回。
// 该切片是有序的，第一个 URL 参数也是切片中的第一个值。
// 因此，按索引读取值是安全的。
type Params []Param

// Get 返回键名与给定名称匹配的第一个 Param 的值，并返回一个布尔值 true。
// 如果未找到匹配的 Param，则返回空字符串和布尔值 false。
func (ps Params) Get(name string) (string, bool) {
	for _, entry := range ps {
		if entry.Key == name {
			return entry.Value, true
		}
	}
	return "", false
}

// ByName 返回键名与给定名称匹配的第一个 Param 的值。
// 如果未找到匹配的 Param，则返回空字符串。
func (ps Params) ByName(name string) (va string) {
	va, _ = ps.Get(name) // 调用 Get 方法获取值，忽略第二个返回值
	return
}

// methodTree 表示特定 HTTP 方法的路由树。
type methodTree struct {
	method string // HTTP 方法（例如 "GET", "POST"）
	root   *node  // 该方法的根节点
}

// methodTrees 是 methodTree 的切片。
type methodTrees []methodTree

// get 根据给定的 HTTP 方法查找并返回对应的根节点。
// 如果找不到，则返回 nil。
func (trees methodTrees) get(method string) *node {
	for _, tree := range trees {
		if tree.method == method {
			return tree.root
		}
	}
	return nil
}

// longestCommonPrefix 计算两个字符串的最长公共前缀的长度。
func longestCommonPrefix(a, b string) int {
	i := 0
	max_ := min(len(a), len(b))    // 找出两个字符串中较短的长度
	for i < max_ && a[i] == b[i] { // 遍历直到达到较短长度或字符不匹配
		i++
	}
	return i // 返回公共前缀的长度
}

// addChild 添加一个子节点，并将通配符子节点（如果存在）保持在数组的末尾。
func (n *node) addChild(child *node) {
	if n.wildChild && len(n.children) > 0 {
		// 如果当前节点有通配符子节点，且已有子节点，则将通配符子节点移到末尾
		wildcardChild := n.children[len(n.children)-1]
		n.children = append(n.children[:len(n.children)-1], child, wildcardChild)
	} else {
		// 否则，直接添加子节点
		n.children = append(n.children, child)
	}
}

// countParams 计算路径中参数（冒号）和捕获所有（星号）的数量。
func countParams(path string) uint16 {
	var n uint16
	s := StringToBytes(path)              // 将路径字符串转换为字节切片
	n += uint16(bytes.Count(s, strColon)) // 统计冒号的数量
	n += uint16(bytes.Count(s, strStar))  // 统计星号的数量
	return n
}

// countSections 计算路径中斜杠（'/'）的数量，即路径段的数量。
func countSections(path string) uint16 {
	s := StringToBytes(path)                // 将路径字符串转换为字节切片
	return uint16(bytes.Count(s, strSlash)) // 统计斜杠的数量
}

// nodeType 定义了节点的类型。
type nodeType uint8

const (
	static   nodeType = iota // 静态节点，路径中不包含参数或通配符
	root                     // 根节点
	param                    // 参数节点（例如:name）
	catchAll                 // 捕获所有节点（例如*path）
)

// node 表示路由树中的一个节点。
type node struct {
	path      string        // 当前节点的路径段
	indices   string        // 子节点第一个字符的索引字符串，用于快速查找子节点
	wildChild bool          // 是否包含通配符子节点（:param 或 *catchAll）
	nType     nodeType      // 节点的类型（静态、根、参数、捕获所有）
	priority  uint32        // 节点的优先级，用于查找时优先匹配
	children  []*node       // 子节点切片，最多有一个 :param 风格的节点位于数组末尾
	handlers  HandlersChain // 绑定到此节点的处理函数链
	fullPath  string        // 完整路径，用于调试和错误信息
}

// incrementChildPrio 增加给定子节点的优先级并在必要时重新排序。
func (n *node) incrementChildPrio(pos int) int {
	cs := n.children         // 获取子节点切片
	cs[pos].priority++       // 增加指定位置子节点的优先级
	prio := cs[pos].priority // 获取新的优先级

	// 调整位置（向前移动）
	newPos := pos
	// 从当前位置向前遍历，如果前一个子节点的优先级小于当前子节点，则交换位置
	for ; newPos > 0 && cs[newPos-1].priority < prio; newPos-- {
		// 交换节点位置
		cs[newPos-1], cs[newPos] = cs[newPos], cs[newPos-1]
	}

	// 构建新的索引字符字符串
	if newPos != pos {
		// 如果位置发生变化，则重新构建 indices 字符串
		// 前缀部分 + 移动的索引字符 + 剩余部分
		n.indices = n.indices[:newPos] + // 未改变的前缀，可能为空
			n.indices[pos:pos+1] + // 被移动的索引字符
			n.indices[newPos:pos] + n.indices[pos+1:] // 除去原位置字符的其余部分
	}

	return newPos // 返回新的位置
}

// addRoute 为给定路径添加一个带有处理函数的节点。
// 非并发安全！
func (n *node) addRoute(path string, handlers HandlersChain) {
	fullPath := path // 记录完整的路径
	n.priority++     // 增加当前节点的优先级

	// 如果是空树（根节点）
	if len(n.path) == 0 && len(n.children) == 0 {
		n.insertChild(path, fullPath, handlers) // 直接插入子节点
		n.nType = root                          // 设置为根节点类型
		return
	}

	parentFullPathIndex := 0 // 记录父节点的完整路径索引

walk: // 外部循环用于遍历和构建路由树
	for {
		// 找到最长公共前缀。
		// 这也意味着公共前缀不包含 ':' 或 '*'，因为现有键不能包含这些字符。
		i := longestCommonPrefix(path, n.path)

		// 分裂边 (Split edge)
		// 如果公共前缀小于当前节点的路径长度，说明当前节点需要被分裂
		if i < len(n.path) {
			child := node{
				path:      n.path[i:],     // 子节点路径是当前节点路径的剩余部分
				wildChild: n.wildChild,    // 继承通配符子节点状态
				nType:     static,         // 分裂后的新节点是静态类型
				indices:   n.indices,      // 继承索引
				children:  n.children,     // 继承子节点
				handlers:  n.handlers,     // 继承处理函数
				priority:  n.priority - 1, // 优先级减1，因为分裂会降低优先级
				fullPath:  n.fullPath,     // 继承完整路径
			}

			n.children = []*node{&child} // 当前节点现在只有一个子节点：新分裂出的子节点
			// 将当前节点的 indices 设置为新子节点路径的第一个字符
			n.indices = BytesToString([]byte{n.path[i]})  // []byte 用于正确的 Unicode 字符转换
			n.path = path[:i]                             // 当前节点的路径更新为公共前缀
			n.handlers = nil                              // 当前节点不再有处理函数（因为它被分裂了）
			n.wildChild = false                           // 当前节点不再是通配符子节点
			n.fullPath = fullPath[:parentFullPathIndex+i] // 更新完整路径
		}

		// 将新节点作为当前节点的子节点
		// 如果路径仍然有剩余部分（即未完全匹配）
		if i < len(path) {
			path = path[i:] // 移除已匹配的前缀
			c := path[0]    // 获取剩余路径的第一个字符

			// '/' 在参数之后
			// 如果当前节点是参数类型，且剩余路径以 '/' 开头，并且只有一个子节点
			// 则继续遍历其唯一的子节点
			if n.nType == param && c == '/' && len(n.children) == 1 {
				parentFullPathIndex += len(n.path) // 更新父节点完整路径索引
				n = n.children[0]                  // 移动到子节点
				n.priority++                       // 增加子节点优先级
				continue walk                      // 继续外部循环
			}

			// 检查是否存在以下一个路径字节开头的子节点
			for i, max_ := 0, len(n.indices); i < max_; i++ {
				if c == n.indices[i] { // 如果找到匹配的索引字符
					parentFullPathIndex += len(n.path) // 更新父节点完整路径索引
					i = n.incrementChildPrio(i)        // 增加子节点优先级并重新排序
					n = n.children[i]                  // 移动到匹配的子节点
					continue walk                      // 继续外部循环
				}
			}

			// 否则，插入新节点
			// 如果第一个字符不是 ':' 也不是 '*'，且当前节点不是 catchAll 类型
			if c != ':' && c != '*' && n.nType != catchAll {
				// 将新字符添加到索引字符串
				n.indices += BytesToString([]byte{c}) // []byte 用于正确的 Unicode 字符转换
				child := &node{
					fullPath: fullPath, // 设置子节点的完整路径
				}
				n.addChild(child)                        // 添加新子节点
				n.incrementChildPrio(len(n.indices) - 1) // 增加新子节点的优先级并重新排序
				n = child                                // 移动到新子节点
			} else if n.wildChild {
				// 正在插入一个通配符节点，需要检查是否与现有通配符冲突
				n = n.children[len(n.children)-1] // 移动到现有的通配符子节点
				n.priority++                      // 增加其优先级

				// 检查通配符是否匹配
				// 如果剩余路径长度大于等于通配符节点的路径长度，且通配符节点路径是剩余路径的前缀
				// 并且不是 catchAll 类型（不能有子路由），
				// 并且通配符之后没有更多字符或紧跟着 '/'
				if len(path) >= len(n.path) && n.path == path[:len(n.path)] &&
					// 不能向 catchAll 添加子节点
					n.nType != catchAll &&
					// 检查更长的通配符，例如 :name 和 :names
					(len(n.path) >= len(path) || path[len(n.path)] == '/') {
					continue walk // 继续外部循环
				}

				// 通配符冲突
				pathSeg := path
				if n.nType != catchAll {
					pathSeg, _, _ = strings.Cut(pathSeg, "/") // 如果不是 catchAll，则截取到下一个 '/'
				}
				prefix := fullPath[:strings.Index(fullPath, pathSeg)] + n.path // 构造冲突前缀
				panic("'" + pathSeg +                                          // 抛出 panic 表示通配符冲突
					"' in new path '" + fullPath +
					"' conflicts with existing wildcard '" + n.path +
					"' in existing prefix '" + prefix +
					"'")
			}

			n.insertChild(path, fullPath, handlers) // 插入子节点（可能包含通配符）
			return                                  // 完成添加路由
		}

		// 否则，将处理函数添加到当前节点
		if n.handlers != nil {
			panic("handlers are already registered for path '" + fullPath + "'") // 如果已注册处理函数，则报错
		}
		n.handlers = handlers // 设置处理函数
		n.fullPath = fullPath // 设置完整路径
		return                // 完成添加路由
	}
}

// findWildcard 搜索通配符段并检查名称是否包含无效字符。
// 如果未找到通配符，则返回 -1 作为索引。
func findWildcard(path string) (wildcard string, i int, valid bool) {
	// 查找开始位置
	escapeColon := false // 是否正在处理转义字符
	for start, c := range []byte(path) {
		if escapeColon {
			escapeColon = false
			if c == ':' { // 如果转义字符是 ':'，则跳过
				continue
			}
			panic("invalid escape string in path '" + path + "'") // 无效的转义字符串
		}
		if c == '\\' { // 如果是反斜杠，则设置转义标志
			escapeColon = true
			continue
		}
		// 通配符以 ':' (参数) 或 '*' (捕获所有) 开头
		if c != ':' && c != '*' {
			continue
		}

		// 查找结束位置并检查无效字符
		valid = true // 默认为有效
		for end, c := range []byte(path[start+1:]) {
			switch c {
			case '/': // 如果遇到斜杠，说明通配符段结束
				return path[start : start+1+end], start, valid
			case ':', '*': // 如果在通配符段中再次遇到 ':' 或 '*'，则无效
				valid = false
			}
		}
		return path[start:], start, valid // 返回找到的通配符、起始索引和有效性
	}
	return "", -1, false // 未找到通配符
}

// insertChild 插入一个带有处理函数的节点。
// 此函数处理包含通配符的路径插入逻辑。
func (n *node) insertChild(path string, fullPath string, handlers HandlersChain) {
	for {
		// 找到第一个通配符之前的前缀
		wildcard, i, valid := findWildcard(path)
		if i < 0 { // 未找到通配符，结束循环
			break
		}

		// 通配符名称只能包含一个 ':' 或 '*' 字符
		if !valid {
			panic("only one wildcard per path segment is allowed, has: '" +
				wildcard + "' in path '" + fullPath + "'") // 报错：每个路径段只允许一个通配符
		}

		// 检查通配符是否有名称
		if len(wildcard) < 2 {
			panic("wildcards must be named with a non-empty name in path '" + fullPath + "'") // 报错：通配符必须有非空名称
		}

		if wildcard[0] == ':' { // 如果是参数节点 (param)
			if i > 0 {
				// 在当前通配符之前插入前缀
				n.path = path[:i] // 当前节点路径更新为前缀
				path = path[i:]   // 剩余路径去除前缀
			}

			child := &node{
				nType:    param,    // 子节点类型为参数
				path:     wildcard, // 子节点路径为通配符名称
				fullPath: fullPath, // 设置子节点的完整路径
			}
			n.addChild(child)  // 添加子节点
			n.wildChild = true // 当前节点标记为有通配符子节点
			n = child          // 移动到新创建的参数节点
			n.priority++       // 增加优先级

			// 如果路径不以通配符结束，则会有一个以 '/' 开头的子路径
			if len(wildcard) < len(path) {
				path = path[len(wildcard):] // 剩余路径去除通配符部分

				child := &node{
					priority: 1,        // 新子节点优先级
					fullPath: fullPath, // 设置子节点的完整路径
				}
				n.addChild(child) // 添加子节点（通常是斜杠后的静态部分）
				n = child         // 移动到这个新子节点
				continue          // 继续循环，查找下一个通配符或结束
			}

			// 否则，我们已经完成。将处理函数插入到新叶节点中
			n.handlers = handlers // 设置处理函数
			return                // 完成
		}

		// 如果是捕获所有节点 (catchAll)
		if i+len(wildcard) != len(path) {
			panic("catch-all routes are only allowed at the end of the path in path '" + fullPath + "'") // 报错：捕获所有路由只能在路径末尾
		}

		// 检查路径段冲突
		if len(n.path) > 0 && n.path[len(n.path)-1] == '/' {
			pathSeg := ""
			if len(n.children) != 0 {
				pathSeg, _, _ = strings.Cut(n.children[0].path, "/")
			}
			panic("catch-all wildcard '" + path + // 报错：捕获所有通配符与现有路径段冲突
				"' in new path '" + fullPath +
				"' conflicts with existing path segment '" + pathSeg +
				"' in existing prefix '" + n.path + pathSeg +
				"'")
		}

		// 当前固定宽度为 1，用于 '/'
		i--
		if i < 0 || path[i] != '/' {
			panic("no / before catch-all in path '" + fullPath + "'") // 报错：捕获所有之前没有 '/'
		}

		n.path = path[:i] // 当前节点路径更新为 catchAll 之前的部分

		// 第一个节点：路径为空的 catchAll 节点
		child := &node{
			wildChild: true,     // 标记为有通配符子节点
			nType:     catchAll, // 类型为 catchAll
			fullPath:  fullPath, // 设置完整路径
		}

		n.addChild(child)       // 添加子节点
		n.indices = string('/') // 索引设置为 '/'
		n = child               // 移动到新创建的 catchAll 节点
		n.priority++            // 增加优先级

		// 第二个节点：包含变量的节点
		child = &node{
			path:     path[i:], // 路径为 catchAll 的实际路径段
			nType:    catchAll, // 类型为 catchAll
			handlers: handlers, // 设置处理函数
			priority: 1,        // 优先级
			fullPath: fullPath, // 设置完整路径
		}
		n.children = []*node{child} // 将其设为当前节点的唯一子节点

		return // 完成
	}

	// 如果没有找到通配符，简单地插入路径和处理函数
	n.path = path         // 设置当前节点路径
	n.handlers = handlers // 设置处理函数
	n.fullPath = fullPath // 设置完整路径
}

// nodeValue 包含 (*Node).getValue 方法的返回值
type nodeValue struct {
	handlers HandlersChain // 匹配到的处理函数链
	params   *Params       // 提取的 URL 参数
	tsr      bool          // 是否建议进行尾部斜杠重定向 (Trailing Slash Redirect)
	fullPath string        // 匹配到的完整路径
}

// skippedNode 结构体用于在 getValue 查找过程中记录跳过的节点信息，以便回溯。
type skippedNode struct {
	path        string // 跳过时的当前路径
	node        *node  // 跳过的节点
	paramsCount int16  // 跳过时已收集的参数数量
}

// getValue 返回注册到给定路径（key）的处理函数。通配符的值会保存到 map 中。
// 如果找不到处理函数，则在存在一个带有额外（或不带）尾部斜杠的处理函数时，
// 建议进行 TSR（尾部斜杠重定向）。
func (n *node) getValue(path string, params *Params, skippedNodes *[]skippedNode, unescape bool) (value nodeValue) {
	var globalParamsCount int16 // 全局参数计数

walk: // 外部循环用于遍历路由树
	for {
		prefix := n.path // 当前节点的路径前缀
		if len(path) > len(prefix) {
			if path[:len(prefix)] == prefix { // 如果路径以当前节点的前缀开头
				path = path[len(prefix):] // 移除已匹配的前缀

				// 优先尝试所有非通配符子节点，通过匹配索引字符
				idxc := path[0] // 剩余路径的第一个字符
				for i, c := range []byte(n.indices) {
					if c == idxc { // 如果找到匹配的索引字符
						// 如果当前节点有通配符子节点，则将当前节点添加到 skippedNodes，以便回溯
						if n.wildChild {
							index := len(*skippedNodes)
							*skippedNodes = (*skippedNodes)[:index+1]
							(*skippedNodes)[index] = skippedNode{
								path: prefix + path, // 记录跳过的路径
								node: &node{ // 复制当前节点的状态
									path:      n.path,
									wildChild: n.wildChild,
									nType:     n.nType,
									priority:  n.priority,
									children:  n.children,
									handlers:  n.handlers,
									fullPath:  n.fullPath,
								},
								paramsCount: globalParamsCount, // 记录当前参数计数
							}
						}

						n = n.children[i] // 移动到匹配的子节点
						continue walk     // 继续外部循环
					}
				}

				if !n.wildChild {
					// 如果路径在循环结束时不等于 '/' 且当前节点没有子节点
					// 当前节点需要回溯到最后一个有效的 skippedNode
					if path != "/" {
						for length := len(*skippedNodes); length > 0; length-- {
							skippedNode := (*skippedNodes)[length-1]
							*skippedNodes = (*skippedNodes)[:length-1]     // 弹出 skippedNode
							if strings.HasSuffix(skippedNode.path, path) { // 如果跳过的路径包含当前路径
								path = skippedNode.path // 恢复路径
								n = skippedNode.node    // 恢复节点
								if value.params != nil {
									*value.params = (*value.params)[:skippedNode.paramsCount] // 恢复参数切片
								}
								globalParamsCount = skippedNode.paramsCount // 恢复参数计数
								continue walk                               // 继续外部循环
							}
						}
					}

					// 未找到。
					// 如果存在一个带有额外（或不带）尾部斜杠的处理函数，
					// 我们可以建议重定向到相同 URL，不带尾部斜杠。
					value.tsr = path == "/" && n.handlers != nil // 如果路径是 "/" 且当前节点有处理函数，则建议 TSR
					return value
				}

				// 处理通配符子节点，它总是位于数组的末尾
				n = n.children[len(n.children)-1] // 移动到通配符子节点
				globalParamsCount++               // 增加全局参数计数

				switch n.nType {
				case param: // 参数节点
					// 查找参数结束位置（'/' 或路径末尾）
					end := 0
					for end < len(path) && path[end] != '/' {
						end++
					}

					// 保存参数值
					if params != nil {
						// 如果需要，预分配容量
						if cap(*params) < int(globalParamsCount) {
							newParams := make(Params, len(*params), globalParamsCount)
							copy(newParams, *params)
							*params = newParams
						}

						if value.params == nil {
							value.params = params
						}
						// 在预分配的容量内扩展切片
						i := len(*value.params)
						*value.params = (*value.params)[:i+1] // 扩展切片
						val := path[:end]                     // 提取参数值
						if unescape {                         // 如果需要进行 URL 解码
							if v, err := url.QueryUnescape(val); err == nil {
								val = v // 解码成功则更新值
							}
						}
						(*value.params)[i] = Param{ // 存储参数
							Key:   n.path[1:], // 参数键名（去除冒号）
							Value: val,        // 参数值
						}
					}

					// 我们需要继续深入！
					if end < len(path) {
						if len(n.children) > 0 {
							path = path[end:] // 移除已提取的参数部分
							n = n.children[0] // 移动到下一个子节点
							continue walk     // 继续外部循环
						}

						// ... 但我们无法继续
						value.tsr = len(path) == end+1 // 如果路径只剩下斜杠，则建议 TSR
						return value
					}

					if value.handlers = n.handlers; value.handlers != nil {
						value.fullPath = n.fullPath
						return value // 如果当前节点有处理函数，则返回
					}
					if len(n.children) == 1 {
						// 未找到处理函数。检查是否存在此路径加尾部斜杠的处理函数，以进行 TSR 建议
						n = n.children[0]
						value.tsr = (n.path == "/" && n.handlers != nil) || (n.path == "" && n.indices == "/")
					}
					return value

				case catchAll: // 捕获所有节点
					// 保存参数值
					if params != nil {
						// 如果需要，预分配容量
						if cap(*params) < int(globalParamsCount) {
							newParams := make(Params, len(*params), globalParamsCount)
							copy(newParams, *params)
							*params = newParams
						}

						if value.params == nil {
							value.params = params
						}
						// 在预分配的容量内扩展切片
						i := len(*value.params)
						*value.params = (*value.params)[:i+1] // 扩展切片
						val := path                           // 参数值是剩余的整个路径
						if unescape {                         // 如果需要进行 URL 解码
							if v, err := url.QueryUnescape(path); err == nil {
								val = v // 解码成功则更新值
							}
						}
						(*value.params)[i] = Param{ // 存储参数
							Key:   n.path[2:], // 参数键名（去除星号）
							Value: val,        // 参数值
						}
					}

					value.handlers = n.handlers // 设置处理函数
					value.fullPath = n.fullPath
					return value // 返回

				default:
					panic("invalid node type") // 无效的节点类型
				}
			}
		}

		if path == prefix { // 如果路径完全匹配当前节点的前缀
			// 如果当前路径不等于 '/' 且节点没有注册的处理函数，且最近匹配的节点有子节点
			// 当前节点需要回溯到最后一个有效的 skippedNode
			if n.handlers == nil && path != "/" {
				for length := len(*skippedNodes); length > 0; length-- {
					skippedNode := (*skippedNodes)[length-1]
					*skippedNodes = (*skippedNodes)[:length-1]
					if strings.HasSuffix(skippedNode.path, path) {
						path = skippedNode.path
						n = skippedNode.node
						if value.params != nil {
							*value.params = (*value.params)[:skippedNode.paramsCount]
						}
						globalParamsCount = skippedNode.paramsCount
						continue walk
					}
				}
			}
			// 我们应该已经到达包含处理函数的节点。
			// 检查此节点是否注册了处理函数。
			if value.handlers = n.handlers; value.handlers != nil {
				value.fullPath = n.fullPath
				return value // 如果有处理函数，则返回
			}

			// 如果此路由没有处理函数，但此路由有通配符子节点，
			// 则此路径必须有一个带有额外尾部斜杠的处理函数。
			if path == "/" && n.wildChild && n.nType != root {
				value.tsr = true // 建议 TSR
				return value
			}

			if path == "/" && n.nType == static {
				value.tsr = true // 如果是静态节点且路径是根，则建议 TSR
				return value
			}

			// 未找到处理函数。检查此路径加尾部斜杠是否存在处理函数，以进行尾部斜杠重定向建议
			for i, c := range []byte(n.indices) {
				if c == '/' { // 如果索引中包含 '/'
					n = n.children[i]                                      // 移动到对应的子节点
					value.tsr = (len(n.path) == 1 && n.handlers != nil) || // 如果子节点路径是 '/' 且有处理函数
						(n.nType == catchAll && n.children[0].handlers != nil) // 或者子节点是 catchAll 且其子节点有处理函数
					return value
				}
			}

			return value
		}

		// 未找到。我们可以建议重定向到相同 URL，添加一个额外的尾部斜杠，
		// 如果该路径的叶节点存在。
		value.tsr = path == "/" || // 如果路径是根路径
			(len(prefix) == len(path)+1 && prefix[len(path)] == '/' && // 或者前缀比路径多一个斜杠
				path == prefix[:len(prefix)-1] && n.handlers != nil) // 且路径是前缀去掉最后一个斜杠，且有处理函数

		// 回溯到最后一个有效的 skippedNode
		if !value.tsr && path != "/" {
			for length := len(*skippedNodes); length > 0; length-- {
				skippedNode := (*skippedNodes)[length-1]
				*skippedNodes = (*skippedNodes)[:length-1]
				if strings.HasSuffix(skippedNode.path, path) {
					path = skippedNode.path
					n = skippedNode.node
					if value.params != nil {
						*value.params = (*value.params)[:skippedNode.paramsCount]
					}
					globalParamsCount = skippedNode.paramsCount
					continue walk
				}
			}
		}

		return value // 返回未找到
	}
}

// findCaseInsensitivePath 对给定路径进行不区分大小写的查找，并尝试找到处理函数。
// 它还可以选择修复尾部斜杠。
// 它返回大小写校正后的路径和一个布尔值，指示查找是否成功。
func (n *node) findCaseInsensitivePath(path string, fixTrailingSlash bool) ([]byte, bool) {
	const stackBufSize = 128 // 栈上缓冲区的默认大小

	// 在常见情况下使用栈上静态大小的缓冲区。
	// 如果路径太长，则在堆上分配缓冲区。
	buf := make([]byte, 0, stackBufSize)
	if length := len(path) + 1; length > stackBufSize {
		buf = make([]byte, 0, length) // 如果路径太长，则分配更大的缓冲区
	}

	ciPath := n.findCaseInsensitivePathRec(
		path,
		buf,              // 预分配足够的内存给新路径
		[4]byte{},        // 空的 rune 缓冲区
		fixTrailingSlash, // 是否修复尾部斜杠
	)

	return ciPath, ciPath != nil // 返回校正后的路径和是否成功找到
}

// shiftNRuneBytes 将字节数组中的字节向左移动 n 个字节。
func shiftNRuneBytes(rb [4]byte, n int) [4]byte {
	switch n {
	case 0:
		return rb
	case 1:
		return [4]byte{rb[1], rb[2], rb[3], 0} // 移动1位
	case 2:
		return [4]byte{rb[2], rb[3]} // 移动2位
	case 3:
		return [4]byte{rb[3]} // 移动3位
	default:
		return [4]byte{} // 其他情况返回空
	}
}

// findCaseInsensitivePathRec 由 n.findCaseInsensitivePath 使用的递归不区分大小写查找函数。
func (n *node) findCaseInsensitivePathRec(path string, ciPath []byte, rb [4]byte, fixTrailingSlash bool) []byte {
	npLen := len(n.path) // 当前节点的路径长度

walk: // 外部循环用于遍历路由树
	// 只要剩余路径长度大于等于当前节点路径长度，且当前节点路径（除第一个字符外）不区分大小写匹配剩余路径
	for len(path) >= npLen && (npLen == 0 || strings.EqualFold(path[1:npLen], n.path[1:])) {
		// 将公共前缀添加到结果中
		oldPath := path                    // 保存原始路径
		path = path[npLen:]                // 移除已匹配的前缀
		ciPath = append(ciPath, n.path...) // 将当前节点的路径添加到不区分大小写路径中

		if len(path) == 0 { // 如果路径已完全匹配
			// 我们应该已经到达包含处理函数的节点。
			// 检查此节点是否注册了处理函数。
			if n.handlers != nil {
				return ciPath // 如果有处理函数，则返回校正后的路径
			}

			// 未找到处理函数。
			// 尝试通过添加尾部斜杠来修复路径
			if fixTrailingSlash {
				for i, c := range []byte(n.indices) {
					if c == '/' { // 如果索引中包含 '/'
						n = n.children[i]                             // 移动到对应的子节点
						if (len(n.path) == 1 && n.handlers != nil) || // 如果子节点路径是 '/' 且有处理函数
							(n.nType == catchAll && n.children[0].handlers != nil) { // 或者子节点是 catchAll 且其子节点有处理函数
							return append(ciPath, '/') // 返回添加斜杠后的路径
						}
						return nil // 否则返回 nil
					}
				}
			}
			return nil // 未找到，返回 nil
		}

		// 如果此节点没有通配符（参数或捕获所有）子节点，
		// 我们可以直接查找下一个子节点并继续遍历树。
		if !n.wildChild {
			// 跳过已处理的 rune 字节
			rb = shiftNRuneBytes(rb, npLen)

			if rb[0] != 0 {
				// 旧 rune 未处理完
				idxc := rb[0]
				for i, c := range []byte(n.indices) {
					if c == idxc {
						// 继续处理子节点
						n = n.children[i]
						npLen = len(n.path)
						continue walk // 继续外部循环
					}
				}
			} else {
				// 处理一个新的 rune
				var rv rune

				// 查找 rune 的开始位置。
				// Runes 最长为 4 字节。
				// -4 肯定会是另一个 rune。
				var off int
				for max_ := min(npLen, 3); off < max_; off++ {
					if i := npLen - off; utf8.RuneStart(oldPath[i]) {
						// 从缓存路径读取 rune
						rv, _ = utf8.DecodeRuneInString(oldPath[i:])
						break
					}
				}

				// 计算当前 rune 的小写字节
				lo := unicode.ToLower(rv)
				utf8.EncodeRune(rb[:], lo) // 将小写 rune 编码到缓冲区

				// 跳过已处理的字节
				rb = shiftNRuneBytes(rb, off)

				idxc := rb[0]
				for i, c := range []byte(n.indices) {
					// 小写匹配
					if c == idxc {
						// 必须使用递归方法，因为大写字节和小写字节都可能作为索引存在
						if out := n.children[i].findCaseInsensitivePathRec(
							path, ciPath, rb, fixTrailingSlash,
						); out != nil {
							return out // 如果找到，则返回
						}
						break
					}
				}

				// 如果未找到匹配项，则对大写 rune 执行相同操作（如果它不同）
				if up := unicode.ToUpper(rv); up != lo {
					utf8.EncodeRune(rb[:], up) // 将大写 rune 编码到缓冲区
					rb = shiftNRuneBytes(rb, off)

					idxc := rb[0]
					for i, c := range []byte(n.indices) {
						// 大写匹配
						if c == idxc {
							// 继续处理子节点
							n = n.children[i]
							npLen = len(n.path)
							continue walk // 继续外部循环
						}
					}
				}
			}

			// 未找到。我们可以建议重定向到相同 URL，不带尾部斜杠，
			// 如果该路径的叶节点存在。
			if fixTrailingSlash && path == "/" && n.handlers != nil {
				return ciPath // 如果可以修复尾部斜杠且有处理函数，则返回
			}
			return nil // 未找到，返回 nil
		}

		n = n.children[0] // 移动到通配符子节点（通常是唯一一个）
		switch n.nType {
		case param: // 参数节点
			// 查找参数结束位置（'/' 或路径末尾）
			end := 0
			for end < len(path) && path[end] != '/' {
				end++
			}

			// 将参数值添加到不区分大小写路径中
			ciPath = append(ciPath, path[:end]...)

			// 我们需要继续深入！
			if end < len(path) {
				if len(n.children) > 0 {
					// 继续处理子节点
					n = n.children[0]
					npLen = len(n.path)
					path = path[end:]
					continue // 继续外部循环
				}

				// ... 但我们无法继续
				if fixTrailingSlash && len(path) == end+1 {
					return ciPath // 如果可以修复尾部斜杠且路径只剩下斜杠，则返回
				}
				return nil // 未找到，返回 nil
			}

			if n.handlers != nil {
				return ciPath // 如果有处理函数，则返回
			}

			if fixTrailingSlash && len(n.children) == 1 {
				// 未找到处理函数。检查此路径加尾部斜杠是否存在处理函数
				n = n.children[0]
				if n.path == "/" && n.handlers != nil {
					return append(ciPath, '/') // 返回添加斜杠后的路径
				}
			}

			return nil // 未找到，返回 nil

		case catchAll: // 捕获所有节点
			return append(ciPath, path...) // 返回添加剩余路径后的路径（捕获所有）

		default:
			panic("invalid node type") // 无效的节点类型
		}
	}

	// 未找到。
	// 尝试通过添加/删除尾部斜杠来修复路径
	if fixTrailingSlash {
		if path == "/" {
			return ciPath // 如果路径是根路径，则返回
		}
		// 如果路径长度比当前节点路径少一个斜杠，且末尾是斜杠，
		// 且不区分大小写匹配，且当前节点有处理函数
		if len(path)+1 == npLen && n.path[len(path)] == '/' &&
			strings.EqualFold(path[1:], n.path[1:len(path)]) && n.handlers != nil {
			return append(ciPath, n.path...) // 返回添加当前节点路径后的路径
		}
	}
	return nil // 未找到，返回 nil
}
