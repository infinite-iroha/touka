# Touka Logger 接口迁移方案

## 基于 Go 1.26 `go:fix inline` 的自动化迁移设计

---

## 一、问题分析

当前架构问题：
```
Engine.LogReco          → *reco.Logger (公开字段, 直接访问)
Context.GetLogger()     → 返回 *reco.Logger (具体类型)
Context.Debugf/Infof... → 硬编码 c.engine.LogReco.Debugf(...)
```

这导致用户无法替换日志实现（如 zap/logrus）。

---

## 二、目标架构

```
Engine.logger           → Logger 接口 (私有)
Engine.LogReco          → *reco.Logger (公开, Deprecated - 保持向后兼容)
Engine.GetLogger()      → 返回 Logger 接口
Engine.SetLogger(Logger)→ 设置日志实现
Context.GetLogger()     → 返回 Logger 接口
Context.Debugf/Infof... → 调用 c.engine.logger.Debugf(...)
```

---

## 三、Logger 接口定义

```go
// logger.go
package touka

// Logger 是日志接口，支持任意日志库实现
type Logger interface {
    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)
    Fatalf(format string, args ...any)
    Panicf(format string, args ...any)
}

// CloserLogger 可选扩展，支持关闭操作
type CloserLogger interface {
    Logger
    Close() error
}
```

---

## 四、Engine 结构变更

```go
// engine.go 变更
type Engine struct {
    // ... 其他字段保持不变

    // logger 是新的日志接口 (私有)
    logger Logger

    // logReco 是保留的 reco.Logger 引用 (私有)
    // 用于向后兼容，当通过 SetLoggerReco 设置时同步到 logger
    logReco *reco.Logger

    // 其他字段...
}
```

新增/修改方法：

```go
// GetLogger 返回日志接口
func (engine *Engine) GetLogger() Logger {
    return engine.logger
}

// SetLogger 设置任意 Logger 实现
func (engine *Engine) SetLogger(l Logger) {
    engine.logger = l
    // 如果是 *reco.Logger 类型，同步更新 logReco
    if rl, ok := l.(*reco.Logger); ok {
        engine.logReco = rl
    } else {
        engine.logReco = nil
    }
}

// SetLoggerCfg 使用 reco.Config 配置日志
func (engine *Engine) SetLoggerCfg(logcfg reco.Config) {
    logger := NewLogger(logcfg)
    engine.logger = logger
    engine.logReco = logger
}
```

---

## 五、`go:fix inline` 兼容性函数

### 5.1 旧 API 包装函数

在 `compat.go` 中定义：

```go
// compat.go
package touka

import "github.com/fenthope/reco"

// GetLogReco 返回 reco.Logger，用于向后兼容
//
//go:fix inline
func (engine *Engine) GetLogReco() *reco.Logger {
    return engine.logReco
}

// SetLogReco 设置 reco.Logger，用于向后兼容
//
//go:fix inline
func (engine *Engine) SetLogReco(l *reco.Logger) {
    engine.logReco = l
    engine.logger = l
}
```

### 5.2 Context 日志方法的 inline 包装

```go
// context_compat.go
package touka

// Debugf 记录 Debug 级别日志
//
//go:fix inline
func (c *Context) Debugf(format string, args ...any) {
    c.engine.logger.Debugf(format, args...)
}

// Infof 记录 Info 级别日志
//
//go:fix inline
func (c *Context) Infof(format string, args ...any) {
    c.engine.logger.Infof(format, args...)
}

// Warnf 记录 Warn 级别日志
//
//go:fix inline
func (c *Context) Warnf(format string, args ...any) {
    c.engine.logger.Warnf(format, args...)
}

// Errorf 记录 Error 级别日志
//
//go:fix inline
func (c *Context) Errorf(format string, args ...any) {
    c.engine.logger.Errorf(format, args...)
}

// Fatalf 记录 Fatal 级别日志
//
//go:fix inline
func (c *Context) Fatalf(format string, args ...any) {
    c.engine.logger.Fatalf(format, args...)
}

// Panicf 记录 Panic 级别日志
//
//go:fix inline
func (c *Context) Panicf(format string, args ...any) {
    c.engine.logger.Panicf(format, args...)
}
```

### 5.3 GetLogger 返回类型的兼容处理

由于 `GetLogger()` 返回类型从 `*reco.Logger` 变为 `Logger`，需要提供兼容函数：

```go
// context_compat.go (续)

// GetLoggerReco 返回 *reco.Logger 类型，用于需要具体类型的场景
//
//go:fix inline
func (c *Context) GetLoggerReco() *reco.Logger {
    if rl, ok := c.engine.logger.(*reco.Logger); ok {
        return rl
    }
    return nil
}
```

---

## 六、go:fix inline 工作原理

### 迁移前用户代码：
```go
func handler(c *touka.Context) {
    // 旧 API 调用
    c.Debugf("request: %s", c.Request.URL.Path)
    c.engine.LogReco.Infof("server started")
}
```

### go fix 执行后（自动替换）：
```go
func handler(c *touka.Context) {
    // Debugf 被替换为函数体
    c.engine.logger.Debugf("request: %s", c.Request.URL.Path)

    // LogReco 访问无法通过 inline 自动处理，需要手动迁移
    // 或者通过 getter 调用
}
```

### 对于字段访问的处理策略：

`engine.LogReco` 字段访问无法直接用 `go:fix inline` 处理，采用以下策略：

1. **保留字段但标记 deprecated**：继续导出 `LogReco` 但文档标记为 deprecated
2. **提供 getter/setter**：通过 `go:fix inline` 提供 `GetLogReco/SetLogReco`
3. **渐进迁移**：用户可以在方便时手动迁移到 `GetLogger()/SetLogger()`

---

## 七、迁移前后对比

### 场景 1：基本日志调用

**迁移前：**
```go
func myHandler(c *touka.Context) {
    c.Debugf("processing request %s", c.Request.URL.Path)
    c.Infof("user %s logged in", username)
    c.Warnf("slow query: %v", duration)
    c.Errorf("db error: %v", err)
}
```

**迁移后（自动替换）：**
```go
func myHandler(c *touka.Context) {
    c.engine.logger.Debugf("processing request %s", c.Request.URL.Path)
    c.engine.logger.Infof("user %s logged in", username)
    c.engine.logger.Warnf("slow query: %v", duration)
    c.engine.logger.Errorf("db error: %v", err)
}
```

### 场景 2：Engine 配置日志

**迁移前：**
```go
engine := touka.New()
engine.LogReco = myLogger  // 直接赋值
logger := engine.LogReco   // 直接读取
```

**迁移后（手动 + 自动混合）：**
```go
engine := touka.New()

// 方式 1：使用新 API（推荐）
engine.SetLogger(myLogger)
logger := engine.GetLogger()

// 方式 2：通过 go:fix inline 自动替换为 getter
// engine.SetLogReco(myLogger)   ← go fix 替换
// logger := engine.GetLogReco()  ← go fix 替换
```

### 场景 3：使用第三方日志库（新功能）

```go
import "go.uber.org/zap"

func main() {
    zapLogger, _ := zap.NewProduction()
    defer zapLogger.Sync()

    engine := touka.New()
    // 使用 zap 替代默认的 reco.Logger
    engine.SetLogger(&ZapAdapter{logger: zapLogger})

    engine.GET("/api", func(c *touka.Context) {
        c.Infof("api called")  // 自动使用 zap 输出
    })
}

// ZapAdapter 适配 zap 到 touka.Logger 接口
type ZapAdapter struct {
    logger *zap.Logger
}

func (z *ZapAdapter) Debugf(format string, args ...any) {
    z.logger.Debug(fmt.Sprintf(format, args...))
}

func (z *ZapAdapter) Infof(format string, args ...any) {
    z.logger.Info(fmt.Sprintf(format, args...))
}

func (z *ZapAdapter) Warnf(format string, args ...any) {
    z.logger.Warn(fmt.Sprintf(format, args...))
}

func (z *ZapAdapter) Errorf(format string, args ...any) {
    z.logger.Error(fmt.Sprintf(format, args...))
}

func (z *ZapAdapter) Fatalf(format string, args ...any) {
    z.logger.Fatal(fmt.Sprintf(format, args...))
}

func (z *ZapAdapter) Panicf(format string, args ...any) {
    z.logger.Panic(fmt.Sprintf(format, args...))
}
```

---

## 八、内部使用迁移

框架内部代码也需要迁移，将直接调用 `engine.LogReco` 改为 `engine.logger`：

需要修改的文件：
- `context.go`: writeResponseBody 中的 `c.engine.LogReco.Errorf`
- `recovery.go`: 如有使用日志
- `logreco.go`: CloseLogger 方法

```go
// context.go 修改前
func (c *Context) writeResponseBody(data []byte, contextMsg string) {
    if _, err := c.Writer.Write(data); err != nil {
        if c.engine.LogReco != nil {
            c.engine.LogReco.Errorf("%s: %v", contextMsg, err)
        }
    }
}

// context.go 修改后
func (c *Context) writeResponseBody(data []byte, contextMsg string) {
    if _, err := c.Writer.Write(data); err != nil {
        if c.engine.logger != nil {
            c.engine.logger.Errorf("%s: %v", contextMsg, err)
        }
    }
}
```

---

## 九、完整文件结构

```
touka/
├── logger.go           # Logger 接口定义
├── logreco.go          # reco.Logger 相关工具函数
├── compat.go           # go:fix inline 兼容性函数 (Engine)
├── context_compat.go   # go:fix inline 兼容性函数 (Context)
├── engine.go           # Engine 结构变更
├── context.go          # Context 日志方法变更
└── ...
```

---

## 十、版本策略

| 版本 | 变更内容 |
|------|---------|
| v1.x | 引入 Logger 接口，LogReco 标记 deprecated |
| v2.x | 移除 LogReco 公开字段，仅通过 getter/setter 访问 |
| v3.x | 移除 go:fix inline 兼容函数 |

---

## 十一、go:fix inline 限制说明

1. **字段访问无法自动迁移**：`engine.LogReco` 字段访问需要用户手动修改
2. **返回类型变更需谨慎**：`GetLogger()` 返回类型变更会导致依赖具体类型的代码失败
3. **inline 函数有大小限制**：函数体过大会影响内联效果
4. **跨包迁移**：`go:fix inline` 支持跨包，但用户必须运行 `go fix`

---

## 十二、推荐迁移步骤

1. **框架侧**：添加 Logger 接口，添加 go:fix inline 函数
2. **用户侧**：运行 `go fix ./...` 自动迁移可处理的部分
3. **用户侧**：手动将 `engine.LogReco` 字段访问改为 `engine.SetLogger()/GetLogger()`
4. **用户侧**：如需使用第三方日志，实现 Logger 接口并通过 SetLogger 设置
