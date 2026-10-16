package application

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dujiao-next/internal/modules/upload/contract"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/google/uuid"
)

var allowedUploadScenes = map[string]struct{}{
	"product":  {},
	"post":     {},
	"banner":   {},
	"editor":   {},
	"common":   {},
	"category": {},
	"telegram": {},
	"reseller": {},
}

// Service 文件上传服务。
type Service struct {
	policy Policy
	store  contract.Store
}

// Policy 定义文件上传校验策略。
type Policy struct {
	MaxSize           int64
	AllowedTypes      []string
	AllowedExtensions []string
	MaxWidth          int
	MaxHeight         int
}

func newUploadValidationError(format string, args ...interface{}) error {
	return &contract.ValidationError{Message: fmt.Sprintf(format, args...)}
}

// NewService 创建文件上传服务实例。
func NewService(policy Policy, store contract.Store) *Service {
	if store == nil {
		panic("upload service: store is nil")
	}
	return &Service{policy: policy, store: store}
}

// SaveFileWithMeta 保存上传的文件并返回完整元数据
func (s *Service) SaveFileWithMeta(file *multipart.FileHeader, scene string) (*contract.Result, error) {
	normalizedScene := normalizeUploadScene(scene)

	// 验证文件大小
	if file == nil || file.Size <= 0 {
		return nil, newUploadValidationError("文件为空")
	}
	if file.Size > s.policy.MaxSize {
		return nil, newUploadValidationError("文件大小超过限制（最大 %d MB）", s.policy.MaxSize/1024/1024)
	}

	// 获取文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	allowedMIME, knownExtension := passiveUploadTypes[ext]
	if !knownExtension || (normalizedScene != "telegram" && !strings.HasPrefix(allowedMIME, "image/")) {
		return nil, newUploadValidationError("文件扩展名不被允许: %s", ext)
	}
	if normalizedScene != "telegram" && len(s.policy.AllowedExtensions) > 0 && !isAllowedExtension(ext, s.policy.AllowedExtensions) {
		return nil, newUploadValidationError("文件扩展名不被允许: %s", ext)
	}

	// 验证文件类型
	src, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	// 读取文件头部识别 MIME 类型
	buffer := make([]byte, 512)
	n, err := src.Read(buffer)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if _, err := src.Seek(0, 0); err != nil { // 重置文件读取位置
		return nil, err
	}

	contentType := http.DetectContentType(buffer[:n])
	if contentType != allowedMIME {
		return nil, newUploadValidationError("文件内容与扩展名不匹配: %s", ext)
	}
	if normalizedScene != "telegram" && len(s.policy.AllowedTypes) > 0 {
		allowed := false
		for _, t := range s.policy.AllowedTypes {
			if strings.EqualFold(contentType, t) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, newUploadValidationError("文件类型不被允许: %s", contentType)
		}
	}

	var imgWidth, imgHeight int
	if strings.HasPrefix(contentType, "image/") && contentType != "image/svg+xml" {
		if _, err := src.Seek(0, 0); err != nil {
			return nil, err
		}
		width, height, err := decodeImageDimensions(src, contentType)
		if err != nil {
			return nil, newUploadValidationError("%s", err.Error())
		}
		imgWidth = width
		imgHeight = height
		if s.policy.MaxWidth > 0 && width > s.policy.MaxWidth {
			return nil, newUploadValidationError("图片宽度超过限制（最大 %d）", s.policy.MaxWidth)
		}
		if s.policy.MaxHeight > 0 && height > s.policy.MaxHeight {
			return nil, newUploadValidationError("图片高度超过限制（最大 %d）", s.policy.MaxHeight)
		}
	}

	if _, err := src.Seek(0, 0); err != nil {
		return nil, err
	}
	// 生成唯一文件名
	filename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
	now := time.Now()
	year := now.Format("2006")
	month := now.Format("01")
	storedURL, err := s.store.Save(contract.StoreInput{
		Source:   src,
		Scene:    normalizedScene,
		Year:     year,
		Month:    month,
		Filename: filename,
	})
	if err != nil {
		return nil, err
	}

	return &contract.Result{
		URL:      storedURL,
		Filename: file.Filename,
		MimeType: contentType,
		Size:     file.Size,
		Width:    imgWidth,
		Height:   imgHeight,
	}, nil
}

func normalizeUploadScene(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "common"
	}
	if _, ok := allowedUploadScenes[value]; ok {
		return value
	}
	return "common"
}

func isAllowedExtension(ext string, allowed []string) bool {
	for _, allowedExt := range allowed {
		normalized := strings.ToLower(strings.TrimSpace(allowedExt))
		if normalized == "" {
			continue
		}
		if !strings.HasPrefix(normalized, ".") {
			normalized = "." + normalized
		}
		if strings.EqualFold(ext, normalized) {
			return true
		}
	}
	return false
}

func decodeImageDimensions(src io.ReadSeeker, contentType string) (int, int, error) {
	if strings.EqualFold(contentType, "image/webp") {
		width, height, err := decodeWebPDimensions(src)
		if err != nil {
			return 0, 0, fmt.Errorf("无法解析 WebP 图片: %w", err)
		}
		return width, height, nil
	}

	if _, err := src.Seek(0, 0); err != nil {
		return 0, 0, err
	}
	cfg, _, err := image.DecodeConfig(src)
	if err != nil {
		return 0, 0, fmt.Errorf("无法解析图片: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// Public same-origin uploads accept only passive raster images. Telegram also
// accepts a small document/archive allowlist; extension and sniffed MIME must agree.
// SVG, HTML, JavaScript and XML are never accepted, even if configured by mistake.
var passiveUploadTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp",
	".pdf": "application/pdf", ".zip": "application/zip",
	".txt": "text/plain; charset=utf-8", ".csv": "text/plain; charset=utf-8",
}

func decodeWebPDimensions(src io.ReadSeeker) (int, int, error) {
	fileSize, err := src.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, 0, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	var header [12]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return 0, 0, err
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return 0, 0, fmt.Errorf("无效的 WebP 文件头")
	}
	riffEnd := int64(binary.LittleEndian.Uint32(header[4:8])) + 8
	if riffEnd != fileSize || riffEnd < 20 {
		return 0, 0, fmt.Errorf("无效的 WebP RIFF 长度")
	}
	var width, height int
	for offset := int64(12); offset < riffEnd; {
		if riffEnd-offset < 8 {
			return 0, 0, fmt.Errorf("WebP chunk 头不完整")
		}
		var chunkHeader [8]byte
		if _, err := io.ReadFull(src, chunkHeader[:]); err != nil {
			return 0, 0, err
		}
		chunkSize := int64(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		padded := chunkSize + chunkSize%2
		if padded > riffEnd-offset-8 {
			return 0, 0, fmt.Errorf("WebP chunk 超出文件边界")
		}
		chunkType := string(chunkHeader[:4])
		if width == 0 && (chunkType == "VP8X" || chunkType == "VP8 " || chunkType == "VP8L") {
			need := int64(10)
			if chunkType == "VP8L" {
				need = 5
			}
			if chunkSize < need || (chunkType == "VP8X" && chunkSize != 10) {
				return 0, 0, fmt.Errorf("无效的 WebP 图片 chunk 长度")
			}
			var data [10]byte
			if _, err := io.ReadFull(src, data[:need]); err != nil {
				return 0, 0, err
			}
			switch chunkType {
			case "VP8X":
				width = 1 + int(data[4]) + int(data[5])<<8 + int(data[6])<<16
				height = 1 + int(data[7]) + int(data[8])<<8 + int(data[9])<<16
			case "VP8 ":
				if data[0]&1 != 0 || string(data[3:6]) != "\x9d\x01\x2a" {
					return 0, 0, fmt.Errorf("VP8 帧头无效")
				}
				width = int(binary.LittleEndian.Uint16(data[6:8]) & 0x3FFF)
				height = int(binary.LittleEndian.Uint16(data[8:10]) & 0x3FFF)
			case "VP8L":
				if data[0] != 0x2f || data[4]&0xe0 != 0 {
					return 0, 0, fmt.Errorf("VP8L 签名无效")
				}
				bits := binary.LittleEndian.Uint32(data[1:5])
				width = int(bits&0x3FFF) + 1
				height = int((bits>>14)&0x3FFF) + 1
			}
			if width <= 0 || height <= 0 {
				return 0, 0, fmt.Errorf("WebP 尺寸无效")
			}
		}
		offset += 8 + padded
		if _, err := src.Seek(offset, io.SeekStart); err != nil {
			return 0, 0, err
		}
	}
	if width == 0 {
		return 0, 0, fmt.Errorf("WebP 缺少图片 chunk")
	}
	return width, height, nil
}

// MaxRequestBytes bounds multipart parsing, including a small allowance for headers.
func (s *Service) MaxRequestBytes() int64 { return s.policy.MaxSize + (1 << 20) }
