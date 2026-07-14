package main

import (
	"fmt"
	"net/http"

	"github.com/infinite-iroha/touka"
)

func main() {
	r := touka.New()

	// 注册QUERY路由
	// QUERY方法是RFC 10008定义的新HTTP方法
	// 它是安全且幂等的，可以携带请求体，适用于复杂查询场景
	r.QUERY("/search", func(c *touka.Context) {
		// 获取请求体
		body, err := c.GetReqBodyFull()
		if err != nil {
			c.JSON(http.StatusBadRequest, touka.H{
				"error": "Failed to read request body",
			})
			return
		}

		// 返回查询结果
		c.JSON(http.StatusOK, touka.H{
			"method":      c.Request.Method,
			"query_body":  string(body),
			"description": "This is a QUERY request (RFC 10008)",
		})
	})

	// 注册路由组
	api := r.Group("/api").(*touka.RouterGroup)
	{
		// 在路由组中使用QUERY
		api.QUERY("/data", func(c *touka.Context) {
			body, _ := c.GetReqBodyFull()
			c.JSON(http.StatusOK, touka.H{
				"endpoint": "/api/data",
				"payload":  string(body),
			})
		})
	}

	fmt.Println("Server starting on :8080")
	fmt.Println("Try: curl -X QUERY http://localhost:8080/search -d '{\"query\": \"test\"}'")

	if err := r.Run(touka.WithAddr(":8080")); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
