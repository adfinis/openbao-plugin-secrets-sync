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
