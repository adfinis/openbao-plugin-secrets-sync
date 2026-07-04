variable "name_prefix" {
  description = "Prefix for IAM resources created by this manual e2e fixture."
  type        = string
  default     = "openbao-plugin-secrets-sync-e2e"
}

variable "secret_name_prefix" {
  description = "Secrets Manager name prefix the manual test may create, update, read, and delete."
  type        = string
  default     = "openbao-plugin-secrets-sync-manual/"
}

variable "external_id" {
  description = "Optional fixed STS external ID. If omitted, OpenTofu generates one in state."
  type        = string
  default     = null
}

variable "trusted_principal_arns" {
  description = "AWS principal ARNs allowed to assume the test role. Defaults to this account root."
  type        = list(string)
  default     = []
}

variable "github_oidc_enabled" {
  description = "Create a GitHub Actions OIDC role for auth_mode=web_identity e2e validation."
  type        = bool
  default     = false
}

variable "github_oidc_provider_url" {
  description = "GitHub Actions OIDC issuer URL."
  type        = string
  default     = "https://token.actions.githubusercontent.com"
}

variable "github_oidc_audience" {
  description = "OIDC audience accepted by the GitHub Actions web-identity role."
  type        = string
  default     = "sts.amazonaws.com"
}

variable "github_oidc_thumbprint" {
  description = "Thumbprint for the GitHub Actions OIDC provider."
  type        = string
  default     = "6938fd4d98bab03faadb97b34396831e3780aea1"
}

variable "github_oidc_owner" {
  description = "GitHub organization or user that owns the repository."
  type        = string
  default     = "adfinis"
}

variable "github_oidc_repo" {
  description = "GitHub repository allowed to assume the web-identity role."
  type        = string
  default     = "openbao-plugin-secrets-sync"
}

variable "github_oidc_branch" {
  description = "GitHub branch allowed to assume the web-identity role."
  type        = string
  default     = "feat/aws-web-identity-auth"
}

variable "tags" {
  description = "Additional tags for IAM resources."
  type        = map(string)
  default     = {}
}
