package smtp

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/i18n"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/shared/mailbrand"
	"github.com/dujiao-next/internal/telegramidentity"
)

type mailPresentation struct {
	Brand, Mark, Heading, Category, Body, Code, CodeLabel, Note, Footer, LinkLabel, SiteURL, Lang string
}

// Table layout and inline CSS keep transactional mail readable without images,
// remote fonts or scripts. Every value is escaped by html/template.
var transactionTemplate = template.Must(template.New("transaction").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Heading}}</title>
<style>@media only screen and (max-width:560px){.ticket-outer{padding:20px 16px!important}.ticket-header{padding:25px 22px!important}.ticket-brand{font-size:27px!important}.ticket-column{display:block!important;width:100%!important;box-sizing:border-box!important}.ticket-intro{padding:25px 0 22px!important;border-right:0!important}.ticket-content{padding:25px 0!important;border-top:1px dashed #9a9a9a!important}.ticket-code{font-size:36px!important;letter-spacing:1px!important}.ticket-footer{padding-top:20px!important}.ticket-heading{font-size:23px!important}}</style></head>
<body style="margin:0;padding:0;background:#ffffff;color:#171717;font-family:Arial,'PingFang SC','Microsoft YaHei',sans-serif">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#ffffff"><tr><td class="ticket-outer" align="center" style="padding:40px 24px">
<!--[if mso]><table role="presentation" width="680"><tr><td><![endif]-->
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:680px;table-layout:fixed">
<tr><td class="ticket-header" style="padding:31px 32px;background:#171717;color:#ffffff"><p class="ticket-brand" style="margin:0;font-size:34px;line-height:1.25;letter-spacing:2px;font-weight:800;word-break:break-word">{{.Brand}}</p><p style="margin:13px 0 0;font-size:10px;line-height:1.6;letter-spacing:3px;color:#bfbfbf">{{.Category}}</p></td></tr>
<tr><td style="padding:0"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="table-layout:fixed"><tr>
<td class="ticket-column ticket-intro" valign="top" width="35%" style="width:35%;padding:34px 26px 32px 0;border-right:1px dashed #9a9a9a"><h1 class="ticket-heading" style="margin:0 0 21px;font-size:26px;line-height:1.4;letter-spacing:-.5px;font-weight:700;word-break:break-word">{{.Heading}}</h1>
{{if .Code}}<p style="margin:0;font-size:13px;line-height:1.9;color:#707070;white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word">{{.Body}}</p>{{else}}<p style="margin:0;font-size:12px;line-height:1.9;color:#707070;word-break:break-word">{{.Footer}}</p>{{end}}
</td>
<td class="ticket-column ticket-content" valign="top" width="65%" style="width:65%;padding:36px 0 32px 32px">
{{if .Code}}<p style="margin:0 0 16px;font-size:11px;line-height:1.5;letter-spacing:2px;color:#707070">{{.CodeLabel}}</p><div class="ticket-code" style="font-family:'Courier New',monospace;font-size:48px;line-height:1.15;font-weight:700;letter-spacing:3px;color:#111111;word-break:break-all">{{.Code}}</div><p style="margin:25px 0 0;padding-top:19px;border-top:3px solid #171717;font-size:12px;line-height:1.9;color:#707070;white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word">{{.Note}}</p>
{{else}}<div style="font-family:'Courier New','PingFang SC','Microsoft YaHei',monospace;font-size:13px;line-height:1.9;white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word">{{.Body}}</div>{{end}}
</td></tr></table></td></tr>
<tr><td class="ticket-footer" style="padding:23px 0 0;border-top:2px solid #171717">{{if .SiteURL}}<a href="{{.SiteURL}}" style="display:inline-block;padding:12px 22px;background:#171717;color:#ffffff;text-decoration:none;font-size:12px;font-weight:700;line-height:1.5">{{.LinkLabel}} &nbsp;→</a>{{end}}{{if .Code}}<p style="margin:17px 0 0;font-size:10px;line-height:1.8;color:#8b8b8b;word-break:break-word">{{.Footer}}</p>{{end}}</td></tr>
</table><!--[if mso]></td></tr></table><![endif]-->
</td></tr></table></body></html>`))

func presentation(brand mailbrand.Brand, subject, body, locale string) mailPresentation {
	normalized := normalizeLocale(locale)
	name := strings.TrimSpace(brand.SiteName)
	if name == "" {
		name = strings.TrimSpace(brand.FromName)
	}
	if name == "" {
		name = "Store"
	}
	p := mailPresentation{Brand: name, Mark: string([]rune(name)[0]), Heading: strings.TrimPrefix(subject, name+" - "), Body: body, SiteURL: safeMailSiteURL(brand.SiteURL), Lang: normalized,
		Category: "订单通知", Footer: "此邮件由系统自动发送，请妥善保留订单信息。", LinkLabel: "访问商店"}
	if normalized == i18n.LocaleTW {
		p.Category = "訂單通知"
		p.Footer = "此郵件由系統自動發送，請妥善保留訂單資訊。"
		p.LinkLabel = "造訪商店"
	}
	if normalized == i18n.LocaleEN {
		p.Category = "ORDER NOTICE"
		p.Footer = "An automatic update from your store. Please keep your order details."
		p.LinkLabel = "Visit store"
	}
	return p
}

func safeMailSiteURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}

func verificationPresentation(code, purpose, locale, subject string, brand mailbrand.Brand, expiry int) (mailPresentation, string) {
	p := presentation(brand, subject, "", locale)
	if expiry <= 0 {
		expiry = 10
	}
	p.Category = "账户验证"
	p.CodeLabel = "本次验证码"
	p.Footer = "账户服务 · 请勿向他人提供验证码或密码。"
	action := "验证邮箱"
	switch strings.ToLower(strings.TrimSpace(purpose)) {
	case constants.VerifyPurposeRegister:
		action = "创建账户"
	case constants.VerifyPurposeReset:
		action = "重置账户密码"
	case constants.VerifyPurposeTelegramBind:
		action = "绑定 Telegram"
	case constants.VerifyPurposeChangeEmailOld, constants.VerifyPurposeChangeEmailNew:
		action = "更换账户邮箱"
	}
	p.Body = "请在商店页面输入下方验证码，完成" + action + "。"
	p.Note = fmt.Sprintf("%d 分钟内有效，仅可使用一次。\n若非本人操作，请忽略此邮件。", expiry)
	if normalizeLocale(locale) == i18n.LocaleTW {
		p.Category = "帳戶驗證"
		p.CodeLabel = "本次驗證碼"
		p.Footer = "帳戶服務 · 請勿向他人提供驗證碼或密碼。"
		p.Body = "請回到商店頁面輸入下方驗證碼，完成本次操作。"
		p.Note = fmt.Sprintf("%d 分鐘內有效，僅可使用一次。\n若非本人操作，請忽略此郵件。", expiry)
	}
	if normalizeLocale(locale) == i18n.LocaleEN {
		p.Category = "ACCOUNT VERIFICATION"
		p.CodeLabel = "Your verification code"
		p.Footer = "Account service · Never share your code or password."
		p.Body = "Enter this code on the store page to complete your request."
		p.Note = fmt.Sprintf("Valid for %d minutes. Use once only.\nIf you did not request this, you can ignore this email.", expiry)
	}
	p.Code = code
	return p, p.Note
}

func orderMailBrand(input notificationcontract.OrderStatusEmailInput) mailbrand.Brand {
	brand := input.MailBrand
	if strings.TrimSpace(brand.SiteName) == "" {
		brand.SiteName = input.SiteName
	}
	if strings.TrimSpace(brand.SiteURL) == "" {
		brand.SiteURL = input.SiteURL
	}
	return brand
}

func (s *Service) sendPresentedEmail(to, subject, plain string, p mailPresentation, attachmentName, attachment string, brand mailbrand.Brand) error {
	if telegramidentity.IsPlaceholderEmail(to) {
		return nil
	}
	if strings.TrimSpace(brand.FromName) == "" {
		brand.FromName = brand.SiteName
	}
	from, addr, err := s.prepareSMTPEnvelope(to, brand.FromName)
	if err != nil {
		return err
	}
	var html bytes.Buffer
	if err = transactionTemplate.Execute(&html, p); err != nil {
		return err
	}
	message, err := buildPresentedEmailMessage(from, to, subject, plain, html.String(), attachmentName, attachment, brand.ReplyTo)
	if err != nil {
		return err
	}
	return s.sendSMTPMessage(addr, to, []byte(message))
}

// Plain text remains first in multipart/alternative. Attachments keep their
// exact decoded content and sit outside the alternative body in multipart/mixed.
func buildPresentedEmailMessage(from, to, subject, plain, html, attachmentName, attachment, replyTo string) (string, error) {
	var alternative bytes.Buffer
	aw := multipart.NewWriter(&alternative)
	for _, part := range []struct{ kind, body string }{{"text/plain", plain}, {"text/html", html}} {
		header := textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=UTF-8"}, "Content-Transfer-Encoding": {"base64"}}
		w, err := aw.CreatePart(header)
		if err != nil {
			return "", err
		}
		if _, err = fmt.Fprint(w, mailBase64(part.body)); err != nil {
			return "", err
		}
	}
	if err := aw.Close(); err != nil {
		return "", err
	}
	var message bytes.Buffer
	writeStandardHeaders(&message, from, to, subject, replyTo)
	alternativeType := mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": aw.Boundary()})
	if attachmentName == "" || attachment == "" {
		fmt.Fprintf(&message, "Content-Type: %s\r\n\r\n", alternativeType)
		message.Write(alternative.Bytes())
		return message.String(), nil
	}
	mw := multipart.NewWriter(&message)
	fmt.Fprintf(&message, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	body, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {alternativeType}})
	if err != nil {
		return "", err
	}
	if _, err = body.Write(alternative.Bytes()); err != nil {
		return "", err
	}
	w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=UTF-8"}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": attachmentName})}, "Content-Transfer-Encoding": {"base64"}})
	if err != nil {
		return "", err
	}
	if _, err = fmt.Fprint(w, mailBase64(attachment)); err != nil {
		return "", err
	}
	if err = mw.Close(); err != nil {
		return "", err
	}
	return message.String(), nil
}

func mailBase64(raw string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	var out strings.Builder
	for len(encoded) > 76 {
		out.WriteString(encoded[:76])
		out.WriteString("\r\n")
		encoded = encoded[76:]
	}
	out.WriteString(encoded)
	return out.String()
}
