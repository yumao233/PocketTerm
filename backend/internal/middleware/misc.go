// Package middleware contains the gin middlewares used by the Webshell HTTP
// server: panic recovery, request logging and CORS.
//
// The middlewares are intentionally small and dependency-free (apart from gin,
// the response envelope and the standard library) so they can be composed
// freely and tested in isolation.
package middleware

import (
	"log"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/pkg/response"
)

// Recovery catches panics raised by downstream handlers, logs the stack trace
// and aborts the request with a 500 envelope. Unlike response.Fail (which always
// writes HTTP 200 for business errors), panics are genuine server faults and
// deserve an HTTP 500 so clients and proxies do not cache a success status.
//
// Writing the envelope directly (rather than calling response.Fail) keeps the
// HTTP status at 500 while still emitting the standard {code,message,data}
// shape clients expect.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic recovered: %v\n%s", r, debug.Stack())
				c.AbortWithStatusJSON(500, response.Envelope{
					Code:    response.ErrInternal,
					Message: "internal server error",
					Data:    nil,
				})
			}
		}()
		c.Next()
	}
}

// Logger emits one log line per request with method, path, status and latency.
// It uses a timer started before the handler chain runs and reads the final
// status after c.Next() returns.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		log.Printf("%s %s %d %s", c.Request.Method, c.Request.URL.Path, c.Writer.Status(), latency)
	}
}

// CORS sets the cross-origin headers for the given allowed origins. When the
// list is empty no headers are added (same-origin only). When it contains "*"
// the wildcard is echoed (and Allow-Credentials is forced to false, since the
// CORS spec forbids credentials with a wildcard origin). Otherwise the request
// Origin is echoed only if it appears in the list. OPTIONS preflight requests
// are short-circuited with 204 No Content.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	// Normalize once for fast lookup.
	allow := make(map[string]struct{}, len(allowedOrigins))
	wildcard := false
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			wildcard = true
		}
		if o != "" {
			allow[o] = struct{}{}
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		switch {
		case wildcard:
			// Echo wildcard. Credentials must be omitted with "*".
			c.Header("Access-Control-Allow-Origin", "*")
		case origin != "":
			if _, ok := allow[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
				// Credentials only when a concrete origin matched.
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// Preflight short-circuit.
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}