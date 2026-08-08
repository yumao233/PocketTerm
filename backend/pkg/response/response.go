// Package response provides the unified JSON response envelope used across all
// Webshell HTTP handlers.
//
// Every HTTP endpoint (other than the WebSocket upgrade) returns HTTP 200 with a
// body of the form {"code":int,"message":string,"data":any}. A response is
// considered successful when code == ErrOK (0); any other code signals a
// logical error even though the HTTP status remains 200.
package response

import "github.com/gin-gonic/gin"

// Error code constants. These are the values carried in Envelope.Code and are
// stable across the public API.
const (
	ErrOK                  = 0    // success
	ErrNotInitialized      = 1001 // system not yet set up (no admin user)
	ErrAlreadyInitialized  = 1002 // setup attempted after an admin already exists
	ErrInvalidCredentials  = 1003 // wrong username or password
	ErrUnauthenticated     = 1004 // missing or invalid authentication
	ErrForbidden           = 1005 // authenticated but not allowed
	ErrNotFound            = 1006 // resource not found
	ErrInvalidParam        = 1007 // request validation failed
	ErrTemplateNameDuplicate = 2001 // template name already exists
	ErrSessionNotFound     = 3001 // session id not found or not owned by user
	ErrSessionClosed       = 3002 // operation on a session that has already closed
	ErrInternal            = 5000 // unexpected internal error
)

// Envelope is the standard JSON wrapper for all non-WebSocket HTTP responses.
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// OK writes a successful envelope: HTTP 200 with code 0, message "ok" and the
// given data payload (which may be nil).
func OK(c *gin.Context, data any) {
	c.JSON(200, Envelope{Code: ErrOK, Message: "ok", Data: data})
}

// OKMsg writes a successful envelope with a custom message.
func OKMsg(c *gin.Context, msg string, data any) {
	c.JSON(200, Envelope{Code: ErrOK, Message: msg, Data: data})
}

// Fail writes an error envelope: HTTP 200 with the given non-zero code and
// message, and a nil data field.
func Fail(c *gin.Context, code int, msg string) {
	c.JSON(200, Envelope{Code: code, Message: msg, Data: nil})
}

// FailData writes an error envelope that additionally carries a data payload
// (e.g. field-level validation details). Used when extra structured context is
// useful alongside the error code/message.
func FailData(c *gin.Context, code int, msg string, data any) {
	c.JSON(200, Envelope{Code: code, Message: msg, Data: data})
}