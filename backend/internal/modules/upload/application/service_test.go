package application

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"path/filepath"
	"testing"

	"github.com/dujiao-next/internal/modules/upload/contract"
)

type memoryStore struct {
	data []byte
}

func (s *memoryStore) Save(input contract.StoreInput) (string, error) {
	data, err := io.ReadAll(input.Source)
	if err != nil {
		return "", err
	}
	s.data = data
	return "/uploads/" + input.Scene + "/" + input.Year + "/" + input.Month + "/" + input.Filename, nil
}

func TestUploadServiceSaveFileAllowsArchiveForTelegramScene(t *testing.T) {
	policy := Policy{
		MaxSize:           10 * 1024 * 1024,
		AllowedTypes:      []string{"image/jpeg", "image/png"},
		AllowedExtensions: []string{".jpg", ".png"},
	}
	store := &memoryStore{}
	service := NewService(policy, store)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "demo.zip")
	if err != nil {
		t.Fatalf("create form file failed: %v", err)
	}
	if _, err := part.Write([]byte("PK\x03\x04archive fixture")); err != nil {
		t.Fatalf("write form content failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer failed: %v", err)
	}

	reader := multipart.NewReader(&body, writer.Boundary())
	form, err := reader.ReadForm(1024 * 1024)
	if err != nil {
		t.Fatalf("read form failed: %v", err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	files := form.File["file"]
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}

	result, err := service.SaveFileWithMeta(files[0], "telegram")
	if err != nil {
		t.Fatalf("save file failed: %v", err)
	}
	savedPath := result.URL
	if filepath.Ext(savedPath) != ".zip" {
		t.Fatalf("expected .zip saved path, got %s", savedPath)
	}
	if string(store.data) != "PK\x03\x04archive fixture" {
		t.Fatalf("stored content got %q", store.data)
	}
}

func TestUploadRejectsActiveContentAndMismatchedTypesInEveryScene(t *testing.T) {
	for scene := range allowedUploadScenes {
		for _, fixture := range []struct{ name, body string }{
			{"attack.html", "<!doctype html><script>alert(1)</script>"},
			{"attack.js", "alert(1)"}, {"attack.svg", "<svg/>"},
			{"attack.xml", "<?xml version=\"1.0\"?><svg/>"},
			{"attack.png", "<html>not a PNG</html>"},
			{"attack.zip", "<html>not a zip</html>"},
			{"attack.txt", "<html>not plain text</html>"},
		} {
			t.Run(scene+"/"+fixture.name, func(t *testing.T) {
				store := &memoryStore{}
				svc := NewService(Policy{MaxSize: 1 << 20}, store)
				if _, err := svc.SaveFileWithMeta(createMultipartFile(t, fixture.name, []byte(fixture.body)), scene); err == nil {
					t.Fatal("active/mismatched upload accepted")
				}
				if store.data != nil {
					t.Fatal("rejected upload reached storage")
				}
			})
		}
	}
}

func TestUploadSafeFiles(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name, scene string
		body        []byte
	}{
		{"photo.png", "product", pngData.Bytes()},
		{"document.pdf", "telegram", []byte("%PDF-1.7\nfixture")},
		{"notes.txt", "telegram", []byte("plain text notes")},
	} {
		store := &memoryStore{}
		svc := NewService(Policy{MaxSize: 1 << 20}, store)
		if _, err := svc.SaveFileWithMeta(createMultipartFile(t, fixture.name, fixture.body), fixture.scene); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(store.data, fixture.body) {
			t.Fatal("stored file mismatch")
		}
	}
}

type boundedWebPReader struct {
	*bytes.Reader
	maxRead int
}

func (r *boundedWebPReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	return r.Reader.Read(p)
}
func webPFixture(kind string, data []byte) []byte {
	out := make([]byte, 20+len(data)+len(data)%2)
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	copy(out[8:], "WEBP")
	copy(out[12:], kind)
	binary.LittleEndian.PutUint32(out[16:20], uint32(len(data)))
	copy(out[20:], data)
	return out
}
func TestWebPDimensionsBoundedReadsAndChunkValidation(t *testing.T) {
	valid := webPFixture("VP8X", []byte{0, 0, 0, 0, 9, 0, 0, 19, 0, 0})
	for _, fixture := range []struct {
		name   string
		mutate func([]byte)
		trim   int
		valid  bool
	}{
		{"valid", func([]byte) {}, 0, true},
		{"enormous declared chunk", func(b []byte) { binary.LittleEndian.PutUint32(b[16:20], 0xffffffff) }, 0, false},
		{"RIFF too large", func(b []byte) { binary.LittleEndian.PutUint32(b[4:8], 0xffffffff) }, 0, false},
		{"RIFF too short", func(b []byte) { binary.LittleEndian.PutUint32(b[4:8], 4) }, 0, false},
		{"truncated body", func([]byte) {}, 1, false},
		{"VP8X wrong size", func(b []byte) { binary.LittleEndian.PutUint32(b[16:20], 9) }, 0, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			fixture.mutate(data)
			data = data[:len(data)-fixture.trim]
			r := &boundedWebPReader{Reader: bytes.NewReader(data)}
			w, h, err := decodeWebPDimensions(r)
			if (err == nil) != fixture.valid {
				t.Fatalf("dimensions=%dx%d error=%v", w, h, err)
			}
			if fixture.valid && (w != 10 || h != 20) {
				t.Fatalf("dimensions=%dx%d", w, h)
			}
			if r.maxRead > 12 {
				t.Fatalf("attacker-controlled allocation: %d", r.maxRead)
			}
		})
	}
	for _, fixture := range []struct {
		kind string
		data []byte
		w, h int
	}{
		{"VP8 ", []byte{0, 0, 0, 0x9d, 1, 0x2a, 2, 0, 3, 0}, 2, 3},
		{"VP8L", []byte{0x2f, 0, 0, 0, 0}, 1, 1},
	} {
		w, h, err := decodeWebPDimensions(bytes.NewReader(webPFixture(fixture.kind, fixture.data)))
		if err != nil || w != fixture.w || h != fixture.h {
			t.Fatalf("%s: %dx%d %v", fixture.kind, w, h, err)
		}
	}
}

// createMultipartFile 辅助函数：创建模拟的 multipart 文件
func createMultipartFile(t *testing.T, filename string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file failed: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form content failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer failed: %v", err)
	}
	reader := multipart.NewReader(&body, writer.Boundary())
	form, err := reader.ReadForm(1024 * 1024)
	if err != nil {
		t.Fatalf("read form failed: %v", err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	files := form.File["file"]
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	return files[0]
}
