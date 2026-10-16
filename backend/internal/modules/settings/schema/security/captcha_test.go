package settingssecurity

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

func TestAdminCaptchaPatchRoundTripPreservesOtherScenes(t *testing.T) {
	current := DefaultCaptchaSetting(config.CaptchaConfig{
		Provider: constants.CaptchaProviderImage,
		Scenes: config.CaptchaSceneConfig{
			RegisterSendCode: true,
		},
	})
	var patch CaptchaSettingPatch
	if err := json.Unmarshal([]byte(`{"scenes":{"admin_login":true}}`), &patch); err != nil {
		t.Fatal(err)
	}
	next, err := ApplyCaptchaSettingPatch(current, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Scenes.AdminLogin || next.Scenes.Login || !next.Scenes.RegisterSendCode {
		t.Fatalf("patch changed unrelated scenes or lost admin flag: %+v", next.Scenes)
	}
	encoded := EncodeCaptchaSetting(next)
	decoded := DecodeCaptchaSetting(encoded, CaptchaSetting{})
	if decoded.Scenes != next.Scenes {
		t.Fatalf("persistent round trip lost scenes: %+v", decoded.Scenes)
	}
	runtime := CaptchaSettingToConfig(decoded)
	if got := DefaultCaptchaSetting(runtime).Scenes; got != next.Scenes {
		t.Fatalf("runtime round trip lost scenes: %+v", got)
	}
	for name, view := range map[string]jsonmap.JSON{
		"public": PublicCaptchaSetting(next),
		"admin":  MaskCaptchaSettingForAdmin(next),
	} {
		scenes := view["scenes"].(map[string]interface{})
		if scenes["admin_login"] != true || scenes["login"] != false {
			t.Fatalf("%s config does not distinguish admin login: %#v", name, scenes)
		}
	}
	if err := json.Unmarshal([]byte(`{"scenes":{"admin_login":false}}`), &patch); err != nil {
		t.Fatal(err)
	}
	disabled, err := ApplyCaptchaSettingPatch(next, patch)
	if err != nil || disabled.Scenes.AdminLogin || !disabled.Scenes.RegisterSendCode {
		t.Fatalf("explicit false patch failed: %+v, %v", disabled.Scenes, err)
	}
}

func TestAdminCaptchaRequiresProviderAndDefaultsToDisabled(t *testing.T) {
	defaults := DefaultCaptchaSetting(config.CaptchaConfig{})
	if defaults.Scenes.AdminLogin || defaults.IsSceneEnabled(constants.CaptchaSceneAdminLogin) {
		t.Fatal("admin captcha must be opt-in")
	}
	enabled := true
	if _, err := ApplyCaptchaSettingPatch(defaults, CaptchaSettingPatch{
		Scenes: &CaptchaScenePatch{AdminLogin: &enabled},
	}); !errors.Is(err, ErrCaptchaConfigInvalid) {
		t.Fatalf("admin-only enabled scene without provider got %v", err)
	}
	legacy := DecodeCaptchaSetting(jsonmap.JSON{
		"provider": constants.CaptchaProviderImage,
		"scenes":   map[string]interface{}{"login": true},
	}, defaults)
	if !legacy.IsSceneEnabled(constants.CaptchaSceneAdminLogin) || !legacy.IsSceneEnabled(constants.CaptchaSceneLogin) {
		t.Fatal("old persisted login setting must continue protecting both login types")
	}
}
