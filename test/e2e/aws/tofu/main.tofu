data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_region" "current" {}

resource "random_id" "external_id" {
  byte_length = 16
}

locals {
  external_id = (
    var.external_id != null && var.external_id != ""
    ? var.external_id
    : "openbao-plugin-secrets-sync-${random_id.external_id.hex}"
  )
  trusted_principal_arns = (
    length(var.trusted_principal_arns) > 0
    ? var.trusted_principal_arns
    : ["arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"]
  )
  secret_arn_prefix = "arn:${data.aws_partition.current.partition}:secretsmanager:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:secret:${var.secret_name_prefix}*"
  tags = merge(
    {
      ManagedBy = "openbao-plugin-secrets-sync-e2e"
      Purpose   = "manual-openbao-plugin-secrets-sync-test"
    },
    var.tags,
  )
}

data "aws_iam_policy_document" "assume_role" {
  statement {
    sid     = "AllowManualE2EAssumeRole"
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = local.trusted_principal_arns
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [local.external_id]
    }
  }
}

resource "aws_iam_role" "e2e" {
  name               = "${var.name_prefix}-role"
  assume_role_policy = data.aws_iam_policy_document.assume_role.json
  tags               = local.tags
}

data "aws_iam_policy_document" "secrets_manager" {
  statement {
    sid       = "ListSecretsForHealthAndCleanup"
    effect    = "Allow"
    actions   = ["secretsmanager:ListSecrets"]
    resources = ["*"]
  }

  statement {
    sid    = "ManageOnlyManualE2ESecrets"
    effect = "Allow"
    actions = [
      "secretsmanager:CreateSecret",
      "secretsmanager:DeleteSecret",
      "secretsmanager:DescribeSecret",
      "secretsmanager:GetSecretValue",
      "secretsmanager:PutSecretValue",
      "secretsmanager:RestoreSecret",
      "secretsmanager:TagResource",
      "secretsmanager:UntagResource",
    ]
    resources = [local.secret_arn_prefix]
  }
}

resource "aws_iam_policy" "secrets_manager" {
  name   = "${var.name_prefix}-secrets-manager"
  policy = data.aws_iam_policy_document.secrets_manager.json
  tags   = local.tags
}

resource "aws_iam_role_policy_attachment" "secrets_manager" {
  role       = aws_iam_role.e2e.name
  policy_arn = aws_iam_policy.secrets_manager.arn
}
