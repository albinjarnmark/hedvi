package config

import (
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestEnvironmentOwnsOnlyItsExactProjectTenant(t *testing.T) {
	id := uuid.New()
	compact := strings.ReplaceAll(id.String(), "-", "")
	for _, tc := range []struct {
		env    Env
		prefix string
	}{{EnvStaging, "hedvi-"}, {EnvProduction, "hedvi-production-"}, {EnvLocal, "hedvi-"}, {EnvTest, "hedvi-"}} {
		got, err := tc.env.ProjectTenant(id)
		if err != nil || got != tc.prefix+compact || !tc.env.IsCustomerTenant(got) || tc.env.CheckProjectTenant(id, got) != nil {
			t.Fatal(tc.env, err)
		}
		for _, bad := range []string{"", tc.prefix + id.String(), tc.prefix + strings.ToUpper(compact), tc.prefix + strings.Repeat("0", 32), "operator-hedvi-production-auth", "operator-hedvi-staging-auth", "hedvi-system-auth", "hedvi-production-production-" + compact} {
			if tc.env.IsCustomerTenant(bad) {
				t.Fatal("accepted invalid tenant", tc.env, bad)
			}
		}
		if tc.env.CheckProjectTenant(uuid.New(), got) == nil {
			t.Fatal("accepted another project's tenant")
		}
	}
	if EnvStaging.IsCustomerTenant("hedvi-production-"+compact) || EnvProduction.IsCustomerTenant("hedvi-"+compact) {
		t.Fatal("environment boundary failed")
	}
}

func TestTenantPrefixConfiguration(t *testing.T) {
	productionEnv(t)
	for _, tc := range []struct {
		env            Env
		correct, other string
	}{{EnvStaging, "hedvi-", "hedvi-production-"}, {EnvProduction, "hedvi-production-", "hedvi-"}} {
		t.Setenv("HEDVI_ENV", string(tc.env))
		t.Setenv("HEDVI_SES_TENANT_PREFIX", tc.correct)
		if _, err := Load(); err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{tc.other, "", "hedvi-*", "operator-"} {
			t.Setenv("HEDVI_SES_TENANT_PREFIX", value)
			if _, err := Load(); err == nil {
				t.Fatal("accepted invalid prefix", tc.env, value)
			}
		}
	}
}
