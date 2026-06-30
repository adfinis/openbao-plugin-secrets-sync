variable "name_prefix" {
  description = "Prefix for IAM resources created by this manual e2e fixture."
  type        = string
  default     = "openbao-secret-sync-e2e"
}

variable "secret_name_prefix" {
  description = "Secrets Manager name prefix the manual test may create, update, read, and delete."
  type        = string
  default     = "openbao-secret-sync-manual/"
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

variable "tags" {
  description = "Additional tags for IAM resources."
  type        = map(string)
  default     = {}
}
