package adminauthhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	captchaapp "github.com/dujiao-next/internal/modules/captcha/application"
	captchacontract "github.com/dujiao-next/internal/modules/captcha/contract"
	captchahttp "github.com/dujiao-next/internal/modules/captcha/transport/http"
	settingssecurity "github.com/dujiao-next/internal/modules/settings/schema/security"
	"github.com/dujiao-next/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

type captchaTestAuth struct{ calls int }

func (a *captchaTestAuth) Login(_, _ string) (*AuthLoginResult, error) {
	a.calls++
	return nil, ErrInvalidCredentials
}

func (*captchaTestAuth) ChangePassword(uint, string, string) error { return nil }

type captchaTestTurnstile struct{ calls int }

func (v *captchaTestTurnstile) Verify(_ settingssecurity.CaptchaTurnstileSetting, token, _ string) error {
	v.calls++
	if token != "valid-test-token" {
		return captchacontract.ErrInvalid
	}
	return nil
}

func TestAdminLoginChecksDedicatedCaptchaBeforePassword(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scenes     config.CaptchaSceneConfig
		payload    string
		wantAuth   int
		wantVerify int
		wantCode   int
	}{
		{"admin only missing", config.CaptchaSceneConfig{AdminLogin: true}, `{}`, 0, 0, response.CodeBadRequest},
		{"admin only rejected", config.CaptchaSceneConfig{AdminLogin: true}, `{"turnstile_token":"invalid"}`, 0, 1, response.CodeBadRequest},
		{"admin only accepted", config.CaptchaSceneConfig{AdminLogin: true}, `{"turnstile_token":"valid-test-token"}`, 1, 1, response.CodeUnauthorized},
		{"legacy login missing", config.CaptchaSceneConfig{Login: true}, `{}`, 0, 0, response.CodeBadRequest},
		{"disabled unchanged", config.CaptchaSceneConfig{}, `{}`, 1, 0, response.CodeUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &captchaTestAuth{}
			turnstile := &captchaTestTurnstile{}
			service := captchaapp.NewService(nil, config.CaptchaConfig{
				Provider: constants.CaptchaProviderTurnstile,
				Scenes:   tc.scenes,
			}, turnstile)
			handler := NewAdminLoginHandler(auth, captchahttp.NewVerifier(service), nil)
			router := gin.New()
			router.POST("/login", handler.AdminLogin)
			request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"test-admin","password":"test-password","captcha_payload":`+tc.payload+`}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var body response.Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if auth.calls != tc.wantAuth || turnstile.calls != tc.wantVerify || body.StatusCode != tc.wantCode {
				t.Fatalf("got auth=%d verify=%d code=%d, want %d/%d/%d", auth.calls, turnstile.calls, body.StatusCode, tc.wantAuth, tc.wantVerify, tc.wantCode)
			}
		})
	}
}
