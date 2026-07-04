output "region" {
  description = "AWS region used by this OpenTofu fixture."
  value       = data.aws_region.current.name
}

output "role_arn" {
  description = "IAM role ARN to configure on the aws-sm destination."
  value       = aws_iam_role.e2e.arn
}

output "external_id" {
  description = "STS external ID to configure on the aws-sm destination."
  value       = local.external_id
}

output "secret_name_prefix" {
  description = "Secrets Manager name prefix reserved for the manual e2e test."
  value       = var.secret_name_prefix
}

output "make_env" {
  description = "Environment values for make test-e2e-aws."
  value = join(" ", [
    "E2E_AWS_CONFIRM=1",
    "E2E_AWS_REGION=${data.aws_region.current.name}",
    "E2E_AWS_ROLE_ARN=${aws_iam_role.e2e.arn}",
    "E2E_AWS_EXTERNAL_ID=${local.external_id}",
    "E2E_AWS_SECRET_PREFIX=${var.secret_name_prefix}",
  ])
}

output "github_web_identity_role_arn" {
  description = "GitHub Actions web-identity role ARN for the manual workflow."
  value       = var.github_oidc_enabled ? aws_iam_role.github_web_identity[0].arn : null
}

output "github_web_identity_subject" {
  description = "GitHub OIDC subject accepted by the web-identity role."
  value       = var.github_oidc_enabled ? local.github_oidc_subject : null
}

output "github_web_identity_make_env" {
  description = "Environment values for make test-e2e-aws with auth_mode=web_identity."
  value = var.github_oidc_enabled ? join(" ", [
    "E2E_AWS_CONFIRM=1",
    "E2E_AWS_AUTH_MODE=web_identity",
    "E2E_AWS_REGION=${data.aws_region.current.name}",
    "E2E_AWS_ROLE_ARN=${aws_iam_role.github_web_identity[0].arn}",
    "E2E_AWS_SECRET_PREFIX=${var.secret_name_prefix}",
  ]) : null
}
