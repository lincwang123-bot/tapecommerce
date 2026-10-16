package middleware

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUploadFilesRejectActiveLegacyContentAndServeImagesDownloads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"script.js": []byte("alert(1)"), "page.html": []byte("<script>alert(1)</script>"), "vector.svg": []byte("<svg onload=alert(1) />"),
		"photo.png": pngBytes.Bytes(), "document.pdf": []byte("%PDF-1.7"), "notes.txt": []byte("notes"), "archive.zip": []byte("PK\x03\x04"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/uploads/*filepath", UploadFiles(root))
	router.HEAD("/uploads/*filepath", UploadFiles(root))
	for _, name := range []string{"script.js", "page.html", "vector.svg", "link.txt", "", "missing.png", "../secret.txt"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", "/uploads/"+name, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", name, response.Code)
		}
	}
	for _, name := range []string{"photo.png", "document.pdf", "notes.txt", "archive.zip"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", "/uploads/"+name, nil))
		if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), files[name]) {
			t.Fatalf("%s response=%d body=%q", name, response.Code, response.Body.String())
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(response.Header().Get("Content-Security-Policy"), "sandbox;") {
			t.Fatal("missing upload execution protection")
		}
		if name == "photo.png" {
			if response.Header().Get("Content-Disposition") != "" || response.Header().Get("Content-Type") != "image/png" {
				t.Fatal("image rendering disabled")
			}
		} else if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatal("document served inline")
		}
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/uploads/photo.png", nil)
	req.Header.Set("Range", "bytes=0-7")
	router.ServeHTTP(response, req)
	if response.Code != http.StatusPartialContent || response.Body.Len() != 8 {
		t.Fatalf("image range response=%d bytes=%d", response.Code, response.Body.Len())
	}
}
