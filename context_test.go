package touka

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type TestJSON struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func TestGetReqBodyFull_Limit(t *testing.T) {
	smallLimit := int64(10)
	largeBody := "this is a body larger than 10 bytes"
	smallBody := "small"

	// Scenario 1: Request body larger than limit
	t.Run("BodyLargerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeBody))
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		_, err := c.GetReqBodyFull()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size exceeds configured limit (%d bytes)", smallLimit))
	})

	// Scenario 2: ContentLength header larger than limit
	t.Run("ContentLengthLargerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(smallBody)) // Actual body is small
		req.ContentLength = smallLimit + 1
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		_, err := c.GetReqBodyFull()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size (%d bytes) exceeds configured limit (%d bytes)", smallLimit+1, smallLimit))
	})

	// Scenario 3: Request body smaller than limit
	t.Run("BodySmallerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(smallBody))
		req.ContentLength = int64(len(smallBody))
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		bodyBytes, err := c.GetReqBodyFull()
		assert.NoError(t, err)
		assert.Equal(t, smallBody, string(bodyBytes))
	})

	// Scenario 4: Request body slightly larger than limit, but no ContentLength
	// http.MaxBytesReader will still catch this
	t.Run("BodySlightlyLargerNoContentLength", func(t *testing.T) {
		slightlyLargeBody := "elevenbytes" // 11 bytes
		req, _ := http.NewRequest("POST", "/", strings.NewReader(slightlyLargeBody))
		// No ContentLength header
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit) // Limit is 10

		_, err := c.GetReqBodyFull()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size exceeds configured limit (%d bytes)", smallLimit))
	})
}

func TestShouldBindJSON_Limit(t *testing.T) {
	smallLimit := int64(20)
	validJSON := `{"name":"test","value":1}` // approx 25 bytes, check exact
	largeJSON := `{"name":"this is a very long name","value":12345}`
	smallValidJSON := `{"name":"s","v":1}` // small enough

	// Scenario 1: JSON body larger than limit
	t.Run("JSONLargerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeJSON))
		req.Header.Set("Content-Type", "application/json")
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		var data TestJSON
		err := c.ShouldBindJSON(&data)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size exceeds configured limit (%d bytes)", smallLimit))
	})

	// Scenario 2: ContentLength header larger than limit for JSON
	t.Run("ContentLengthLargerThanLimitJSON", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(smallValidJSON)) // Actual body is small
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = smallLimit + 1
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		var data TestJSON
		err := c.ShouldBindJSON(&data)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size (%d bytes) exceeds configured limit (%d bytes)", smallLimit+1, smallLimit))
	})

	// Scenario 3: JSON body smaller than limit
	t.Run("JSONSmallerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(validJSON))
		req.Header.Set("Content-Type", "application/json")
		// Set a limit that is larger than the validJSON
		engineLimit := int64(len(validJSON) + 5)
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(engineLimit)


		var data TestJSON
		err := c.ShouldBindJSON(&data)
		assert.NoError(t, err)
		assert.Equal(t, "test", data.Name)
		assert.Equal(t, 1, data.Value)
	})

	// Scenario 4: JSON body (no content length) slightly larger than limit
	t.Run("JSONSlightlyLargerNoContentLength", func(t *testing.T) {
		// This JSON is `{"name":"abcde","value":1}` which is 24 bytes. Limit is 20.
		slightlyLargeJSON := `{"name":"abcde","value":1}`
		req, _ := http.NewRequest("POST", "/", strings.NewReader(slightlyLargeJSON))
		req.Header.Set("Content-Type", "application/json")
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit) // Limit is 20

		var data TestJSON
		err := c.ShouldBindJSON(&data)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size exceeds configured limit (%d bytes)", smallLimit))
	})
}

func TestMaxRequestBodySize_Disabled(t *testing.T) {
	largeBody := strings.Repeat("a", 20*1024*1024) // 20MB body
	largeJSON := `{"name":"` + strings.Repeat("b", 5*1024*1024) + `","value":1}` // Large JSON

	// Scenario 1: GetReqBodyFull with MaxRequestBodySize = 0
	t.Run("GetReqBodyFull_DisabledZero", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeBody))
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(0) // Disable limit

		bodyBytes, err := c.GetReqBodyFull()
		assert.NoError(t, err)
		assert.Equal(t, largeBody, string(bodyBytes))
	})

	// Scenario 2: GetReqBodyFull with MaxRequestBodySize = -1
	t.Run("GetReqBodyFull_DisabledNegative", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeBody))
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(-1) // Disable limit

		bodyBytes, err := c.GetReqBodyFull()
		assert.NoError(t, err)
		assert.Equal(t, largeBody, string(bodyBytes))
	})

	// Scenario 3: ShouldBindJSON with MaxRequestBodySize = 0
	t.Run("ShouldBindJSON_DisabledZero", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeJSON))
		req.Header.Set("Content-Type", "application/json")
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(0) // Disable limit

		var data TestJSON
		err := c.ShouldBindJSON(&data)
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(data.Name, "bbb")) // Just check prefix of large name
		assert.Equal(t, 1, data.Value)
	})

	// Scenario 4: ShouldBindJSON with MaxRequestBodySize = -1
	t.Run("ShouldBindJSON_DisabledNegative", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeJSON))
		req.Header.Set("Content-Type", "application/json")
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(-1) // Disable limit

		var data TestJSON
		err := c.ShouldBindJSON(&data)
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(data.Name, "bbb"))
		assert.Equal(t, 1, data.Value)
	})
}

// TestGetReqBodyBuffer_Limit (Optional, as logic is very similar to GetReqBodyFull)
// You can add tests for GetReqBodyBuffer if you want explicit coverage,
// but its core limiting logic is identical to GetReqBodyFull.
func TestGetReqBodyBuffer_Limit(t *testing.T) {
	smallLimit := int64(10)
	largeBody := "this is a body larger than 10 bytes"
	smallBody := "small"

	// Scenario 1: Request body larger than limit
	t.Run("BufferBodyLargerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(largeBody))
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		_, err := c.GetReqBodyBuffer()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size exceeds configured limit (%d bytes)", smallLimit))
	})

	// Scenario 2: ContentLength header larger than limit
	t.Run("BufferContentLengthLargerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(smallBody))
		req.ContentLength = smallLimit + 1
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		_, err := c.GetReqBodyBuffer()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("request body size (%d bytes) exceeds configured limit (%d bytes)", smallLimit+1, smallLimit))
	})

	// Scenario 3: Request body smaller than limit
	t.Run("BufferBodySmallerThanLimit", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/", strings.NewReader(smallBody))
		req.ContentLength = int64(len(smallBody))
		c, engine := CreateTestContextWithRequest(httptest.NewRecorder(), req)
		engine.SetMaxRequestBodySize(smallLimit)

		buffer, err := c.GetReqBodyBuffer()
		assert.NoError(t, err)
		assert.Equal(t, smallBody, buffer.String())
	})
}
