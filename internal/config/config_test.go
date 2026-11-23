package config

import "testing"

func TestParseMode(t *testing.T) {
	for _, s := range []string{"all", "web", "worker"} {
		if _, err := ParseMode(s); err != nil {
			t.Errorf("ParseMode(%q): %v", s, err)
		}
	}
	if _, err := ParseMode("both"); err == nil {
		t.Error("invalid mode accepted")
	}
	if !ModeAll.Web() || !ModeAll.Worker() || ModeWeb.Worker() || ModeWorker.Web() {
		t.Error("mode capabilities wrong")
	}
}

func TestWorkerNeedsNoSignInConfiguration(t *testing.T) {
	c := Config{DatabaseURL: "postgres://x"}
	if err := c.Validate(ModeWorker); err != nil {
		t.Errorf("worker: %v", err)
	}
	if err := c.Validate(ModeWeb); err == nil {
		t.Error("web without sign-in accepted")
	}
	if err := (&Config{}).Validate(ModeWorker); err == nil {
		t.Error("worker without a database accepted")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		ok   bool
	}{
		{"no database", Config{AuthDisabled: true}, false},
		{"dev", Config{DatabaseURL: "postgres://x", AuthDisabled: true}, true},
		{"no auth configured", Config{DatabaseURL: "postgres://x"}, false},
		{"oidc without client id", Config{DatabaseURL: "postgres://x", OIDCIssuer: "https://i", BaseURL: "https://a"}, false},
		{"oidc without base url", Config{DatabaseURL: "postgres://x", OIDCIssuer: "https://i", OIDCClientID: "c"}, false},
		{"oidc ok", Config{DatabaseURL: "postgres://x", OIDCIssuer: "https://i", OIDCClientID: "c", BaseURL: "https://a"}, true},
	}
	for _, tc := range cases {
		if err := tc.c.Validate(ModeAll); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
