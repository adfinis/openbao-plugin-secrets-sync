#!/usr/bin/env sh
set -eu

: "${BINARY_PATH:?BINARY_PATH is required}"
: "${BINARY_NAME:?BINARY_NAME is required}"
: "${PLUGIN_VERSION:?PLUGIN_VERSION is required}"
: "${E2E_OCI_IMAGE_IN_BAO:?E2E_OCI_IMAGE_IN_BAO is required}"

E2E_OCI_DIR="${E2E_OCI_DIR:-dist/e2e/oci}"
E2E_OCI_CERT_DIR="${E2E_OCI_CERT_DIR:-${E2E_OCI_DIR}/certs}"
E2E_OCI_CONFIG="${E2E_OCI_CONFIG:-${E2E_OCI_DIR}/openbao.hcl}"
E2E_OCI_PLUGIN_MAX_SIZE="${E2E_OCI_PLUGIN_MAX_SIZE:-134217728}"

mkdir -p "${E2E_OCI_CERT_DIR}" "$(dirname "${E2E_OCI_CONFIG}")"

run_quiet() {
	log="${E2E_OCI_CERT_DIR}/openssl-error.log"
	if ! "$@" >/dev/null 2>"${log}"; then
		cat "${log}" >&2
		exit 1
	fi
	rm -f "${log}"
}

openssl_config="${E2E_OCI_CERT_DIR}/openssl.cnf"
cat >"${openssl_config}" <<'EOF'
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = registry

[v3_req]
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = registry
DNS.2 = localhost
IP.1 = 127.0.0.1
EOF

run_quiet openssl req \
	-x509 \
	-newkey rsa:2048 \
	-nodes \
	-days 7 \
	-sha256 \
	-subj "/CN=openbao-plugin-secrets-sync-e2e-ca" \
	-keyout "${E2E_OCI_CERT_DIR}/ca.key" \
	-out "${E2E_OCI_CERT_DIR}/ca.crt"

run_quiet openssl req \
	-newkey rsa:2048 \
	-nodes \
	-keyout "${E2E_OCI_CERT_DIR}/registry.key" \
	-out "${E2E_OCI_CERT_DIR}/registry.csr" \
	-config "${openssl_config}"

run_quiet openssl x509 \
	-req \
	-in "${E2E_OCI_CERT_DIR}/registry.csr" \
	-CA "${E2E_OCI_CERT_DIR}/ca.crt" \
	-CAkey "${E2E_OCI_CERT_DIR}/ca.key" \
	-CAcreateserial \
	-out "${E2E_OCI_CERT_DIR}/registry.crt" \
	-days 7 \
	-sha256 \
	-extfile "${openssl_config}" \
	-extensions v3_req

chmod 0644 "${E2E_OCI_CERT_DIR}/ca.crt" "${E2E_OCI_CERT_DIR}/registry.crt"
chmod 0600 "${E2E_OCI_CERT_DIR}/ca.key" "${E2E_OCI_CERT_DIR}/registry.key"

sha256sum="$(shasum -a 256 "${BINARY_PATH}" | awk '{print $1}')"

cat >"${E2E_OCI_CONFIG}" <<EOF
plugin_directory = "/openbao/plugins"
plugin_auto_download = true
plugin_auto_register = true
plugin_download_behavior = "fail"
plugin_download_max_size = ${E2E_OCI_PLUGIN_MAX_SIZE}

plugin "secret" "${BINARY_NAME}" {
  image       = "${E2E_OCI_IMAGE_IN_BAO}"
  version     = "${PLUGIN_VERSION}"
  binary_name = "${BINARY_NAME}"
  sha256sum   = "${sha256sum}"
}
EOF

printf 'prepared OCI e2e fixture in %s\n' "${E2E_OCI_DIR}"
