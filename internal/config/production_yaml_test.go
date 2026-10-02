package config

import "testing"

// Guard: config/production.yaml must always load and pass validation
// (catches catalog drift - dead base_url entries, llm.model not matching an
// enabled entry, broken routes - before a Render deploy fails at boot).
// production.yaml reads secrets/JIDs from env vars (Render env); the dummy
// values below keep the test hermetic - never put real JIDs/tokens here
// (this repo is public).
func TestProductionYamlValidates(t *testing.T) {
	for k, v := range map[string]string{
		"ATRIA_API_KEY":                          "dummy",
		"LITELLM_API_KEY":                        "dummy",
		"DISCORD_BOT_TOKEN":                      "dummy",
		"WHATSAPP_ALLOWED_JIDS":                  "dummy@s.whatsapp.net",
		"ADMIN_WHATSAPP_JIDS":                    "dummy@s.whatsapp.net",
		"HEALTH_WHATSAPP_JID":                    "dummy@s.whatsapp.net",
		"WHATSAPP_PROXY_URL":                     "socks5://127.0.0.1:1055",
		"GIPHY_API_KEY":                          "dummy",
		"DATABASE_URL":                           "postgres://user:pass@localhost:5432/db?sslmode=disable",
		"ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET": "dummy",
		"ELEMENT_ORION_EVENT_WEBHOOK_SECRET":     "dummy",
		"LUMEN_EXPORT_KEY":                       "dummy",
		"LUMEN_EXPORT_GITHUB_TOKEN":              "dummy",
	} {
		t.Setenv(k, v)
	}

	cfg, err := Load("../../config/production.yaml")
	if err != nil {
		t.Fatalf("load production.yaml: %v", err)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate production.yaml: %v", err)
	}
	if got := cfg.ResolveLLMModel(); got != "Atria-Dawn-Preview" {
		t.Errorf("ResolveLLMModel = %q, want Atria-Dawn-Preview", got)
	}
}
