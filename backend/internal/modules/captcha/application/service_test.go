package application

import (
	"testing"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/modules/captcha/contract"
	settingssecurity "github.com/dujiao-next/internal/modules/settings/schema/security"
)

type settingReaderStub struct {
	setting settingssecurity.CaptchaSetting
}

func (s settingReaderStub) GetCaptchaSetting(config.CaptchaConfig) (settingssecurity.CaptchaSetting, error) {
	return s.setting, nil
}

type turnstileStub struct {
	cfg      settingssecurity.CaptchaTurnstileSetting
	token    string
	clientIP string
}

func (s *turnstileStub) Verify(cfg settingssecurity.CaptchaTurnstileSetting, token, clientIP string) error {
	s.cfg = cfg
	s.token = token
	s.clientIP = clientIP
	return nil
}

func TestVerifyTurnstileDelegatesToVerifier(t *testing.T) {
	setting := settingssecurity.CaptchaSetting{
		Provider: constants.CaptchaProviderTurnstile,
		Scenes: settingssecurity.CaptchaSceneSetting{
			Login: true,
		},
		Turnstile: settingssecurity.CaptchaTurnstileSetting{
			SecretKey: "secret",
			VerifyURL: "https://captcha.example/verify",
			TimeoutMS: 2000,
		},
	}
	verifier := &turnstileStub{}
	service := NewService(settingReaderStub{setting: setting}, config.CaptchaConfig{}, verifier)

	err := service.Verify(constants.CaptchaSceneLogin, contract.VerifyPayload{TurnstileToken: " token-1 "}, " 127.0.0.1 ")
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if verifier.token != "token-1" || verifier.clientIP != "127.0.0.1" || verifier.cfg.SecretKey != "secret" {
		t.Fatalf("turnstile input mismatch: token=%q ip=%q cfg=%#v", verifier.token, verifier.clientIP, verifier.cfg)
	}
}

func TestVerifyImageRequiresChallengePayload(t *testing.T) {
	setting := settingssecurity.CaptchaSetting{
		Provider: constants.CaptchaProviderImage,
		Scenes: settingssecurity.CaptchaSceneSetting{
			Login: true,
		},
	}
	service := NewService(settingReaderStub{setting: setting}, config.CaptchaConfig{}, &turnstileStub{})
	if err := service.Verify(constants.CaptchaSceneLogin, contract.VerifyPayload{}, ""); err != contract.ErrRequired {
		t.Fatalf("empty image captcha error got %v want ErrRequired", err)
	}
}

func TestVerifySkipsDisabledScene(t *testing.T) {
	setting := settingssecurity.CaptchaSetting{Provider: constants.CaptchaProviderTurnstile}
	service := NewService(settingReaderStub{setting: setting}, config.CaptchaConfig{}, nil)
	if err := service.Verify(constants.CaptchaSceneLogin, contract.VerifyPayload{}, ""); err != nil {
		t.Fatalf("disabled scene must skip captcha, got %v", err)
	}
}

func TestAdminOnlyCaptchaDoesNotRequireCustomerChallenge(t *testing.T) {
	service := NewService(nil, config.CaptchaConfig{
		Provider: constants.CaptchaProviderImage,
		Scenes:   config.CaptchaSceneConfig{AdminLogin: true},
	}, nil)
	if err := service.Verify(constants.CaptchaSceneLogin, contract.VerifyPayload{}, ""); err != nil {
		t.Fatalf("customer login must remain unchanged: %v", err)
	}
	if err := service.Verify(constants.CaptchaSceneAdminLogin, contract.VerifyPayload{}, ""); err != contract.ErrRequired {
		t.Fatalf("admin login must require captcha, got %v", err)
	}
}

func TestLegacyLoginCaptchaStillProtectsAdmin(t *testing.T) {
	service := NewService(nil, config.CaptchaConfig{
		Provider: constants.CaptchaProviderImage,
		Scenes:   config.CaptchaSceneConfig{Login: true},
	}, nil)
	for _, scene := range []string{constants.CaptchaSceneLogin, constants.CaptchaSceneAdminLogin} {
		if err := service.Verify(scene, contract.VerifyPayload{}, ""); err != contract.ErrRequired {
			t.Fatalf("legacy login switch must protect %s, got %v", scene, err)
		}
	}
}

func TestAdminImageCaptchaRejectsMissingWrongAndReplayedAnswers(t *testing.T) {
	service := NewService(nil, config.CaptchaConfig{
		Provider: constants.CaptchaProviderImage,
		Scenes:   config.CaptchaSceneConfig{AdminLogin: true},
	}, nil)
	setting, err := service.getSetting()
	if err != nil {
		t.Fatal(err)
	}
	store := service.ensureImageStore(setting)
	challenge, err := service.GenerateImageChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if challenge.CaptchaID == "" || challenge.ImageBase64 == "" {
		t.Fatal("challenge must contain an ID and an image")
	}
	answer := store.Get(challenge.CaptchaID, false)
	if answer == "" {
		t.Fatal("generated challenge must be stored")
	}
	for name, payload := range map[string]contract.VerifyPayload{
		"missing both": {},
		"missing ID":   {CaptchaCode: answer},
		"missing code": {CaptchaID: challenge.CaptchaID},
	} {
		if err := service.Verify(constants.CaptchaSceneAdminLogin, payload, ""); err != contract.ErrRequired {
			t.Fatalf("%s: got %v, want ErrRequired", name, err)
		}
	}
	payload := contract.VerifyPayload{CaptchaID: challenge.CaptchaID, CaptchaCode: answer}
	if err := service.Verify(constants.CaptchaSceneAdminLogin, payload, ""); err != nil {
		t.Fatalf("valid generated answer rejected: %v", err)
	}
	if err := service.Verify(constants.CaptchaSceneAdminLogin, payload, ""); err != contract.ErrInvalid {
		t.Fatalf("consumed challenge replay got %v, want ErrInvalid", err)
	}
	if err := store.Set("wrong-answer-challenge", "A2b3C"); err != nil {
		t.Fatal(err)
	}
	wrong := contract.VerifyPayload{CaptchaID: "wrong-answer-challenge", CaptchaCode: "wrong"}
	if err := service.Verify(constants.CaptchaSceneAdminLogin, wrong, ""); err != contract.ErrInvalid {
		t.Fatalf("wrong answer got %v, want ErrInvalid", err)
	}
	wrong.CaptchaCode = "A2b3C"
	if err := service.Verify(constants.CaptchaSceneAdminLogin, wrong, ""); err != contract.ErrInvalid {
		t.Fatalf("wrong attempt must also consume challenge, got %v", err)
	}
}
