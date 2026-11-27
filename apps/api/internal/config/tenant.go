package config

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

var ErrProjectTenant = errors.New("SES tenant binding does not match this environment and project")

// The prefix and project ID together identify a tenant in the shared SES account.
func (env Env) CustomerTenantPrefix() string {
	switch env {
	case EnvProduction:
		return "hedvi-production-"
	case EnvStaging, EnvLocal, EnvTest:
		return "hedvi-"
	default:
		return ""
	}
}

func (env Env) ProjectTenant(id uuid.UUID) (string, error) {
	prefix := env.CustomerTenantPrefix()
	if prefix == "" || id == uuid.Nil {
		return "", ErrProjectTenant
	}
	return prefix + strings.ReplaceAll(id.String(), "-", ""), nil
}

func (env Env) CheckProjectTenant(id uuid.UUID, name string) error {
	want, err := env.ProjectTenant(id)
	if err != nil || name != want {
		return ErrProjectTenant
	}
	return nil
}

func (env Env) IsCustomerTenant(name string) bool {
	suffix, ok := strings.CutPrefix(name, env.CustomerTenantPrefix())
	if !ok || len(suffix) != 32 {
		return false
	}
	id, err := uuid.Parse(suffix)
	return err == nil && env.CheckProjectTenant(id, name) == nil
}
