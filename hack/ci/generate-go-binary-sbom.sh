#!/usr/bin/env bash

set -euo pipefail

: "${BINARY_PATH:?BINARY_PATH is required}"
: "${OUTPUT_PATH:?OUTPUT_PATH is required}"
: "${VERSION:?VERSION is required}"
: "${GOOS:?GOOS is required}"
: "${GOARCH:?GOARCH is required}"
: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH is required}"

BINARY_NAME="${BINARY_NAME:-openbao-plugin-secrets-sync}"
GO_BINARY="${GO:-go}"

build_info_json="$(mktemp)"
trap 'rm -f "${build_info_json}"' EXIT

"${GO_BINARY}" version -m -json "${BINARY_PATH}" > "${build_info_json}"

python3 - "${BINARY_PATH}" "${OUTPUT_PATH}" "${VERSION}" "${GOOS}" "${GOARCH}" "${SOURCE_DATE_EPOCH}" "${BINARY_NAME}" "${build_info_json}" <<'PY'
import datetime
import hashlib
import json
import re
import sys
from pathlib import Path


binary_path = Path(sys.argv[1])
output_path = Path(sys.argv[2])
version = sys.argv[3]
goos = sys.argv[4]
goarch = sys.argv[5]
source_date_epoch = int(sys.argv[6])
binary_name = sys.argv[7]
build_info_path = Path(sys.argv[8])

created = datetime.datetime.fromtimestamp(
    source_date_epoch, datetime.timezone.utc
).strftime("%Y-%m-%dT%H:%M:%SZ")

binary_sha = hashlib.sha256(binary_path.read_bytes()).hexdigest()
build_info = json.loads(build_info_path.read_text(encoding="utf-8"))


def spdx_id(value):
    cleaned = re.sub(r"[^A-Za-z0-9.-]", "-", value)
    cleaned = re.sub(r"-+", "-", cleaned).strip("-")
    return cleaned or "unknown"


def package_url(path, version_value):
    suffix = f"@{version_value}" if version_value and version_value != "(devel)" else ""
    return f"pkg:golang/{path}{suffix}"


binary_package_id = f"SPDXRef-Binary-{spdx_id(binary_name)}-{goos}-{goarch}"
binary_filename = binary_path.name

packages = [
    {
        "SPDXID": binary_package_id,
        "name": binary_filename,
        "versionInfo": version,
        "downloadLocation": "NOASSERTION",
        "filesAnalyzed": False,
        "licenseConcluded": "NOASSERTION",
        "licenseDeclared": "NOASSERTION",
        "copyrightText": "NOASSERTION",
        "checksums": [
            {
                "algorithm": "SHA256",
                "checksumValue": binary_sha,
            }
        ],
    }
]

relationships = [
    {
        "spdxElementId": "SPDXRef-DOCUMENT",
        "relationshipType": "DESCRIBES",
        "relatedSpdxElement": binary_package_id,
    }
]

seen = {binary_package_id}
main = build_info.get("Main") or {}
main_path = main.get("Path", "")
if main_path:
    main_version = main.get("Version", "")
    if main_version == "(devel)":
        main_version = version
    package_id = "SPDXRef-GoModule-" + spdx_id(f"{main_path}-{main_version}")
    if package_id not in seen:
        seen.add(package_id)
        packages.append(
            {
                "SPDXID": package_id,
                "name": main_path,
                "versionInfo": main_version or "local",
                "downloadLocation": "NOASSERTION",
                "filesAnalyzed": False,
                "licenseConcluded": "NOASSERTION",
                "licenseDeclared": "NOASSERTION",
                "copyrightText": "NOASSERTION",
                "externalRefs": [
                    {
                        "referenceCategory": "PACKAGE-MANAGER",
                        "referenceType": "purl",
                        "referenceLocator": package_url(main_path, main_version),
                    }
                ],
            }
        )
        relationships.append(
            {
                "spdxElementId": binary_package_id,
                "relationshipType": "GENERATED_FROM",
                "relatedSpdxElement": package_id,
            }
        )

for module in build_info.get("Deps") or []:
    path = module.get("Path", "")
    if not path:
        continue
    module_version = module.get("Version", "")
    replace = module.get("Replace")
    if isinstance(replace, dict) and replace.get("Path"):
        path = replace["Path"]
        module_version = replace.get("Version", module_version)

    package_id = "SPDXRef-GoModule-" + spdx_id(f"{path}-{module_version}")
    if package_id in seen:
        continue
    seen.add(package_id)
    packages.append(
        {
            "SPDXID": package_id,
            "name": path,
            "versionInfo": module_version or "local",
            "downloadLocation": "NOASSERTION",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
            "externalRefs": [
                {
                    "referenceCategory": "PACKAGE-MANAGER",
                    "referenceType": "purl",
                    "referenceLocator": package_url(path, module_version),
                }
            ],
        }
    )
    relationships.append(
        {
            "spdxElementId": binary_package_id,
            "relationshipType": "DEPENDS_ON",
            "relatedSpdxElement": package_id,
        }
    )

packages = sorted(packages, key=lambda item: item["SPDXID"])
relationships = sorted(
    relationships,
    key=lambda item: (
        item["spdxElementId"],
        item["relationshipType"],
        item["relatedSpdxElement"],
    ),
)

namespace_seed = json.dumps(
    {
        "binary_sha": binary_sha,
        "goarch": goarch,
        "goos": goos,
        "packages": packages,
        "relationships": relationships,
        "version": version,
    },
    sort_keys=True,
    separators=(",", ":"),
)
namespace_hash = hashlib.sha256(namespace_seed.encode("utf-8")).hexdigest()

doc = {
    "spdxVersion": "SPDX-2.3",
    "dataLicense": "CC0-1.0",
    "SPDXID": "SPDXRef-DOCUMENT",
    "name": f"{binary_name} {version} {goos}/{goarch}",
    "documentNamespace": f"https://github.com/adfinis/openbao-secret-sync/sbom/{namespace_hash}",
    "creationInfo": {
        "created": created,
        "creators": [
            "Tool: openbao-secret-sync-generate-go-binary-sbom",
        ],
    },
    "packages": packages,
    "relationships": relationships,
}

output_path.parent.mkdir(parents=True, exist_ok=True)
output_path.write_text(
    json.dumps(doc, sort_keys=True, separators=(",", ":")) + "\n",
    encoding="utf-8",
)
PY
