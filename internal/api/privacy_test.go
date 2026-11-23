package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPrivacyPageIsPublicAndMatchesTheHostedCopy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerOps(r, nil) // /privacy needs no database
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/privacy", nil))
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("privacy policy")) {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	hosted, err := os.ReadFile("../../docs/site/privacy.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hosted, privacyPage) {
		t.Error("docs/site/privacy.html and internal/api/privacy.html differ: keep them identical")
	}
}
