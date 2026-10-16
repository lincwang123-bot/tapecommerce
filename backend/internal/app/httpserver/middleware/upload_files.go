package middleware

import (
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// UploadFiles serves legacy and newly uploaded files with the same passive-file
// policy. It never lists directories, follows a link outside root, or serves an
// executable suffix. Documents are downloads on an opaque sandbox origin.
func UploadFiles(root string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Content-Security-Policy", "sandbox; default-src 'none'; base-uri 'none'")
		c.Header("Referrer-Policy", "no-referrer")
		name := strings.TrimPrefix(c.Param("filepath"), "/")
		contentTypes := map[string]string{
			".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
			".pdf": "application/pdf", ".zip": "application/zip", ".txt": "text/plain; charset=utf-8", ".csv": "text/csv; charset=utf-8",
		}
		contentType, allowed := contentTypes[strings.ToLower(path.Ext(name))]
		if !allowed || name == "" || strings.Contains(name, "\\") {
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		directory, err := os.OpenRoot(root)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		defer directory.Close()
		file, err := directory.Open(name)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Header("Content-Type", contentType)
		if !strings.HasPrefix(contentType, "image/") {
			c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
		}
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	}
}
