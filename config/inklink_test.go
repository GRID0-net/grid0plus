package config

import "testing"

func TestDiscordOAuthRejectsPartialConfigurationAndUnsafeCallbacks(t *testing.T) {
	good := DiscordOAuth{ClientID: "123456789012345678", ClientSecret: "test-only", RedirectURI: "http://127.0.0.1:8091/auth/discord/callback"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (DiscordOAuth{}).Validate(); err != nil {
		t.Fatal("disabled OAuth rejected", err)
	}
	for _, callback := range []string{"http://inklink.example.com/auth/discord/callback", "https://example.com/wrong", "https://user:pass@example.com/auth/discord/callback", "https://example.com/auth/discord/callback?next=evil", "https://example.com/auth/discord/callback#fragment"} {
		c := good
		c.RedirectURI = callback
		if c.Validate() == nil {
			t.Fatalf("unsafe callback accepted: %s", callback)
		}
	}
	c := good
	c.ClientSecret = ""
	if c.Validate() == nil {
		t.Fatal("missing secret accepted")
	}
	c.ClientSecretFile = "/private/secret"
	if c.Validate() != nil {
		t.Fatal("secret file rejected")
	}
	c = good
	c.ClientID = "not-a-discord-id"
	if c.Validate() == nil {
		t.Fatal("non numeric client id accepted")
	}
}
