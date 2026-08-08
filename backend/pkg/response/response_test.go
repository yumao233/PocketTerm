package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestOK(t *testing.T) {
	r := gin.New()
	r.GET("/ok", func(c *gin.Context) { OK(c, gin.H{"k": "v"}) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Code != ErrOK {
		t.Errorf("code = %d, want %d", env.Code, ErrOK)
	}
	if env.Message != "ok" {
		t.Errorf("message = %q, want %q", env.Message, "ok")
	}
	m, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data not a map: %T", env.Data)
	}
	if m["k"] != "v" {
		t.Errorf("data.k = %v, want v", m["k"])
	}
}

func TestFail(t *testing.T) {
	r := gin.New()
	r.GET("/fail", func(c *gin.Context) { Fail(c, ErrInvalidParam, "bad input") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fail", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Code != ErrInvalidParam {
		t.Errorf("code = %d, want %d", env.Code, ErrInvalidParam)
	}
	if env.Message != "bad input" {
		t.Errorf("message = %q, want %q", env.Message, "bad input")
	}
	if env.Data != nil {
		t.Errorf("data = %v, want nil", env.Data)
	}
}