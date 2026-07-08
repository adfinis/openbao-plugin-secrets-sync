// Package providerutil contains shared helpers for provider implementations.
package providerutil

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

// Helpers carries provider-specific message prefixes for common provider operations.
type Helpers struct {
	prefix string
}

// New returns helpers that use prefix in validation and provider error messages.
func New(prefix string) Helpers {
	return Helpers{prefix: prefix}
}

// ConfigValue returns the trimmed destination config value for key.
func (Helpers) ConfigValue(cfg providers.DestinationConfig, key string) string {
	if cfg.Config == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Config[key])
}

// BoolConfigValue parses a boolean destination config value or returns fallback when unset.
func (h Helpers) BoolConfigValue(
	cfg providers.DestinationConfig,
	key string,
	fallback bool,
) (bool, error) {
	value := h.ConfigValue(cfg, key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, h.ValidationError(fmt.Sprintf("%s %s must be true or false", h.prefix, key))
	}
	return parsed, nil
}

// ValidationError returns a provider validation error with the supplied message.
func (Helpers) ValidationError(message string) error {
	return &providers.Error{Class: providers.ErrorClassValidation, Message: message}
}

// BlockedPlan returns a blocked plan result using the provider's standard plan-failed message.
func (h Helpers) BlockedPlan(errorClass providers.ErrorClass) *providers.PlanResult {
	return &providers.PlanResult{
		Action:     providers.PlanActionBlocked,
		ErrorClass: errorClass,
		Message:    h.prefix + " provider plan failed",
	}
}

// ProviderError returns a provider runtime error with the provider's standard request-failed message.
func (h Helpers) ProviderError(errorClass providers.ErrorClass) error {
	return &providers.Error{Class: errorClass, Message: h.prefix + " request failed"}
}

// SetupErrorClass maps setup errors to the provider error class contract.
func (Helpers) SetupErrorClass(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	return providers.ErrorClassInternal
}
