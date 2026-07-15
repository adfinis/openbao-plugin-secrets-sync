package backend

import (
	"context"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/version"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathInfo(b *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "info",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback:  b.pathInfoRead,
				Summary:   "Read static secret sync API information.",
				Responses: apiInfoResponse(),
			},
		},
		HelpSynopsis: "Read static secret sync API information.",
		HelpDescription: "Returns static defaults, plugin version, and provider capabilities " +
			"for API clients and operators.",
	}
}

func (b *secretSyncBackend) pathInfoRead(
	_ context.Context,
	_ *logical.Request,
	_ *framework.FieldData,
) (*logical.Response, error) {
	return &logical.Response{Data: newResponseData(
		responseField("plugin_version", version.BuildInfo().Version),
		responseField("defaults", newResponseData(
			responseField("association", associationDefaultsResponse()),
		)),
		responseField("providers", providerInfoResponse(b)),
	)}, nil
}

func providerInfoResponse(b *secretSyncBackend) map[string]interface{} { //nolint:forbidigo
	providerResponses := make(map[string]interface{})
	for _, provider := range b.providerRegistry.Providers() {
		providerResponses[provider.Type()] = newResponseData(
			responseField("capabilities", capabilitiesResponse(provider.Capabilities())),
		)
	}
	return providerResponses
}
