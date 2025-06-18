package touka

type MiddlewareXFunc func() HandlerFunc

// UseChainIf 是一个条件中间件包装器，用于一组中间件
// 如果 `condition` 为 true，它将按顺序创建并执行提供的 `factories` 生成的中间件链
// 否则，它会直接跳过整个链
func (engine *Engine) UseChainIf(condition bool, factories ...MiddlewareXFunc) HandlerFunc {
	// 如果条件不满足或没有提供任何工厂函数，返回一个“穿透”中间件
	if !condition || len(factories) == 0 {
		return func(c *Context) {
			c.Next()
		}
	}

	// 在配置路由时就创建好所有中间件实例
	middlewares := make(HandlersChain, 0, len(factories))
	for _, factory := range factories {
		if factory != nil {
			middlewares = append(middlewares, factory())
		}
	}

	// 返回一个处理器，该处理器负责执行这个子链
	// 这个实现通过临时替换 Context 的处理器链来注入子链，是健壮的
	return func(c *Context) {
		// 将当前的处理链和索引位置保存下来
		originalHandlers := c.handlers
		originalIndex := c.index

		// 创建一个新的临时处理链
		// 它由我们预先创建的 `middlewares` 和一个特殊的“恢复”处理器组成
		subChain := make(HandlersChain, len(middlewares)+1)
		copy(subChain, middlewares)

		// 在子链的末尾添加“恢复”处理器
		//    当所有 `middlewares` 都执行完毕并调用了 Next() 后，这个函数会被执行
		subChain[len(middlewares)] = func(ctx *Context) {
			// 恢复原始的处理链状态
			ctx.handlers = originalHandlers
			ctx.index = originalIndex
			// 继续执行原始处理链中 `UseChainIf` 之后的下一个处理器
			ctx.Next()
		}

		// 将 Context 的处理器链临时替换新的的子链，并重置索引以从头开始
		c.handlers = subChain
		c.index = -1

		c.Next()
	}
}

// UseIf 是一个条件中间件包装器
func (engine *Engine) UseIf(condition bool, middlewareX MiddlewareXFunc) HandlerFunc {
	if !condition {
		return func(c *Context) {
			c.Next()
		}
	}

	// 只有当条件为 true 时，才调用工厂函数创建中间件实例
	// 注意：这会导致每次请求都创建一个新的中间件实例（如果中间件本身有状态）
	// 如果中间件是无状态的，可以进行优化

	// 优化：只创建一次

	return func(c *Context) {
		middleware := middlewareX()
		if middleware != nil {
			middleware(c)
		} else {
			c.Next()
		}
	}
}
