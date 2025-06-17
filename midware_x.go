package touka

// UseIf 是一个条件中间件包装器。
// 如果 `condition` 为 true，它将执行提供的 `middlewares` 链。
// 如果 `condition` 为 false，它会直接跳过这些中间件，调用下一个处理器。
func (engine *Engine) UseIf(condition bool, middlewares ...HandlerFunc) HandlerFunc {
	if !condition {
		return func(c *Context) {
			c.Next()
		}
	}

	// 如果条件为真，返回一个处理器，该处理器负责执行子链。
	// 我们通过修改 Context 的状态来做到这一点。
	// 这比递归闭包或替换 c.Next 要清晰。
	return func(c *Context) {
		// 1. 将当前的处理链和索引位置保存下来。
		originalHandlers := c.handlers
		originalIndex := c.index

		// 2. 创建一个新的处理链，它由传入的中间件和最后的 "恢复并继续" 处理器组成。
		subChain := make(HandlersChain, len(middlewares)+1)
		copy(subChain, middlewares)

		// 3. 在子链的末尾添加一个特殊的 handler。
		//    它的作用是恢复原始的处理链状态，并继续执行原始链中 `If` 之后的处理器。
		subChain[len(middlewares)] = func(ctx *Context) {
			ctx.handlers = originalHandlers
			ctx.index = originalIndex
			ctx.Next()
		}

		// 4. 将 Context 的处理器链临时替换为我们的子链，并重置索引。
		c.handlers = subChain
		c.index = -1

		// 5. 调用 c.Next() 来启动子链的执行。
		c.Next()
	}
}
