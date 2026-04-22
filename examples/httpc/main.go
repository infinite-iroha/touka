package main

import (
	"fmt"
	"net/http"

	"github.com/infinite-iroha/touka"
)

func main() {
	r := touka.Default()

	// 示例 1：简单 GET 请求（自动关联请求 Context）
	r.GET("/proxy", func(c *touka.Context) {
		// 使用 HTTPC() 方法，自动关联请求 Context
		// 当客户端断开连接时，出站请求也会自动取消
		body, err := c.HTTPC().
			GET("https://httpbin.org/get").
			Text()
		if err != nil {
			c.JSON(http.StatusInternalServerError, touka.H{"error": err.Error()})
			return
		}
		c.String(http.StatusOK, "%s", body)
	})

	// 示例 2：带 Header 的 POST 请求
	r.POST("/users", func(c *touka.Context) {
		var req struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
			return
		}

		var result struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}

		// 链式调用，保持 httpc 风格
		// 注意：SetJSONBody 返回 (*RequestBuilder, error)
		rb, err := c.HTTPC().
			POST("https://httpbin.org/post").
			SetHeader("X-API-Key", "secret").
			SetJSONBody(req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, touka.H{"error": err.Error()})
			return
		}
		if err := rb.DecodeJSON(&result); err != nil {
			c.JSON(http.StatusInternalServerError, touka.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	})

	// 示例 3：带查询参数的请求
	r.GET("/search", func(c *touka.Context) {
		query := c.DefaultQuery("q", "")
		page := c.DefaultQuery("page", "1")

		var result struct {
			Items []string `json:"items"`
			Total int      `json:"total"`
		}

		err := c.HTTPC().
			GET("https://httpbin.org/get").
			SetQueryParam("q", query).
			SetQueryParam("page", page).
			DecodeJSON(&result)
		if err != nil {
			c.JSON(http.StatusInternalServerError, touka.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	})

	// 示例 4：使用底层 httpc.Client（旧方式，仍可用但不推荐）
	r.GET("/legacy", func(c *touka.Context) {
		// 旧方式：需要手动 WithContext
		body, err := c.Client().
			GET("https://httpbin.org/get").
			WithContext(c.Context()).
			Text()
		if err != nil {
			c.JSON(http.StatusInternalServerError, touka.H{"error": err.Error()})
			return
		}
		c.String(http.StatusOK, "%s", body)
	})

	fmt.Println("Server running on :8080")
	fmt.Println("Try:")
	fmt.Println("  curl http://localhost:8080/proxy")
	fmt.Println("  curl -X POST -d '{\"name\":\"test\",\"email\":\"test@example.com\"}' http://localhost:8080/users")
	fmt.Println("  curl 'http://localhost:8080/search?q=golang&page=1'")

	// r.Run(touka.WithAddr(":8080"))
}
