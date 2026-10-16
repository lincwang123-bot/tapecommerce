package smtp

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/shared/mailbrand"
	"github.com/dujiao-next/internal/shared/money"
)

type decodedPart struct{ kind, name, body string }

func decodePresented(t *testing.T, header mail.Header, reader io.Reader) []decodedPart {
	t.Helper()
	kind, p, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(kind, "multipart/") {
		mr := multipart.NewReader(reader, p["boundary"])
		var result []decodedPart
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, decodePresented(t, mail.Header(part.Header), part)...)
		}
		return result
	}
	if header.Get("Content-Transfer-Encoding") == "base64" {
		reader = base64.NewDecoder(base64.StdEncoding, reader)
	}
	b, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_, dp, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	return []decodedPart{{kind, dp["filename"], string(b)}}
}

func TestPresentedMIMERetainsTextAndExactAttachment(t *testing.T) {
	plain := "原始卡密：<one>&二\n账号\n密码"
	attachment := strings.Repeat("多行卡密<> &\r\n  保留空格  \n", 200)
	raw, err := buildPresentedEmailMessage(buildFromAddress("noreply@example.test", "扣子电商"), "buyer@example.test", "交付通知", plain, "<p>safe</p>", "交付内容.txt", attachment, "support@example.test")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msg.Header.Get("Content-Type"), "multipart/mixed") {
		t.Fatal("attachment must wrap alternatives")
	}
	parts := decodePresented(t, msg.Header, msg.Body)
	if len(parts) != 3 || parts[0].kind != "text/plain" || parts[0].body != plain || parts[1].kind != "text/html" || parts[1].body != "<p>safe</p>" || parts[2].name != "交付内容.txt" || parts[2].body != attachment {
		t.Fatalf("MIME parts or decoded content changed: %d", len(parts))
	}
	from, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil || from.Name != "扣子电商" {
		t.Fatal("sender encoding")
	}
	if msg.Header.Get("Reply-To") != "support@example.test" {
		t.Fatal("reply-to lost")
	}
	for _, line := range strings.Split(mailBase64(attachment), "\r\n") {
		if len(line) > 76 {
			t.Fatal("overlong base64 line")
		}
	}
}

func TestPresentedHTMLUntrustedContentAndURLs(t *testing.T) {
	for _, badURL := range []string{"javascript:alert(1)", "https://user:password@example.test/", "data:text/html,test", "//example.test/"} {
		p := presentation(mailbrand.Brand{SiteName: "<script>alert(1)</script>", SiteURL: badURL}, "<img src=x>", "card:<b>secret</b>\n第二行", "zh-CN")
		var b bytes.Buffer
		if err := transactionTemplate.Execute(&b, p); err != nil {
			t.Fatal(err)
		}
		html := b.String()
		if strings.Contains(html, "<script>") || strings.Contains(html, "<img src=x>") || strings.Contains(html, "<b>secret</b>") || strings.Contains(html, "href=") {
			t.Fatal("unsafe dynamic markup or URL")
		}
		if !strings.Contains(html, "&lt;b&gt;secret&lt;/b&gt;") {
			t.Fatal("literal delivery content missing")
		}
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		p, note := verificationPresentation("123456", constants.VerifyPurposeReset, locale, "Password reset", mailbrand.Brand{SiteName: "独立商店", SiteURL: "https://shop.example.test/"}, 7)
		if !strings.Contains(note, "7") || p.Code != "123456" || p.SiteURL != "https://shop.example.test/" {
			t.Fatal("verification data changed")
		}
	}
}

// Exercise the public send methods against a sink bound to loopback only.
func TestBrandedSMTPDeliveryLocalSink(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	captured := make(chan string, 4)
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				c := textproto.NewConn(conn)
				_ = c.PrintfLine("220 local-sink")
				for {
					line, err := c.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						_ = c.PrintfLine("250 local-sink")
					case line == "DATA":
						_ = c.PrintfLine("354 continue")
						b, err := io.ReadAll(c.DotReader())
						if err != nil {
							return
						}
						captured <- string(b)
						_ = c.PrintfLine("250 accepted")
					case line == "QUIT":
						_ = c.PrintfLine("221 bye")
						return
					default:
						_ = c.PrintfLine("250 ok")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("SMTP sink did not stop")
		}
	})
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	cfg := &config.EmailConfig{Enabled: true, Host: "127.0.0.1", Port: port, From: "noreply@example.test", FromName: "OLD NAME"}
	cfg.VerifyCode.ExpireMinutes = 7
	service := New(cfg)
	brand := mailbrand.Brand{SiteName: "扣子电商", SiteURL: "https://coco9.cc"}
	if err := service.SendVerifyCode("preview@example.test", "123456", constants.VerifyPurposeRegister, "zh-CN", brand); err != nil {
		t.Fatal(err)
	}
	input := notificationcontract.OrderStatusEmailInput{OrderNo: "PREVIEW-ONLY", Status: constants.OrderStatusCompleted, Currency: "USDT", Amount: money.Amount{}, SiteName: brand.SiteName, SiteURL: brand.SiteURL, MailBrand: brand, FulfillmentInfo: "仅为预览，不可使用\n卡密-A<&>", AttachmentName: "交付.txt", AttachmentContent: "card A\r\n  card B  "}
	if err := service.SendOrderStatusEmail("preview@example.test", input, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var raw string
		select {
		case raw = <-captured:
		case <-time.After(time.Second):
			t.Fatal("mail not captured")
		}
		msg, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		parts := decodePresented(t, msg.Header, msg.Body)
		from, err := mail.ParseAddress(msg.Header.Get("From"))
		if err != nil || from.Name != brand.SiteName {
			t.Fatal("mail sender brand not applied")
		}
		if len(parts) < 2 || parts[0].kind != "text/plain" || parts[1].kind != "text/html" {
			t.Fatal("alternatives missing")
		}
		if !strings.Contains(parts[1].body, brand.SiteName) {
			t.Fatal("brand missing")
		}
		if i == 0 && (!strings.Contains(parts[0].body, "7 分钟") || !strings.Contains(parts[1].body, "123456")) {
			t.Fatal("verification code or configured expiry missing")
		}
		if i == 1 && (len(parts) != 3 || parts[2].body != input.AttachmentContent || !strings.Contains(parts[1].body, "卡密-A&lt;&amp;&gt;")) {
			t.Fatal("delivery escaped or attachment changed")
		}
		if dir := os.Getenv("COCO9_MAIL_PREVIEW_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			name := []string{"verification", "delivery"}[i]
			if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(parts[1].body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".eml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
