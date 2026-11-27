package config

import (
	"strings"
	"testing"
)

func TestSystemMailRequiresDedicatedOriginAndTenant(t *testing.T) {
	good := Config{AuthMailFrom: "accounts@auth.example.com", AuthMailTenant: "operator-auth", AuthMailConfigSet: "operator-auth-events", SESConfigSet: "tenant-events", AppURL: "https://app.example.com"}
	if err := good.validateAuthMail(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.AuthMailFrom = "" },
		func(c *Config) { c.AuthMailFrom = "accounts@example.com" },
		func(c *Config) { c.AuthMailFrom = "Hedvi <accounts@auth.example.com>" },
		func(c *Config) { c.AuthMailFrom = "accounts@auth.example.test" },
		func(c *Config) { c.AuthMailTenant = "hedvi-" + strings.Repeat("a", 32) },
		func(c *Config) { c.AuthMailTenant = "" },
		func(c *Config) { c.AppURL = "https://app.example.com/?redirect=evil" },
		func(c *Config) { c.AppURL = "https://app.example.com/path" },
		func(c *Config) { c.LegalOrigin = "https://hedvi.dev/legal" },
		func(c *Config) { c.LegalOrigin = "http://hedvi.dev" },
		func(c *Config) { c.LegalOrigin = "https://user@hedvi.dev" },
	} {
		c := good
		change(&c)
		if c.validateAuthMail() == nil {
			t.Fatal("unsafe sender config accepted")
		}
	}
}

func TestProductionOriginsAreConfigurable(t *testing.T) {
	c := Config{AuthMailFrom: "accounts@auth.hedvi.dev", AuthMailTenant: "operator-hedvi-production-auth", AuthMailConfigSet: "operator-hedvi-production-auth", SESConfigSet: "customer-events", AppURL: "https://app.hedvi.dev", LegalOrigin: "https://hedvi.dev"}
	if err := c.validateAuthMail(); err != nil {
		t.Fatal(err)
	}
	if c.LegalURLOrigin() != "https://hedvi.dev" {
		t.Fatal(c.LegalURLOrigin())
	}
}
