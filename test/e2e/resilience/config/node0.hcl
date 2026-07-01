ui = false
log_level = "info"

api_addr = "http://127.0.0.1:8200"
cluster_addr = "http://127.0.0.1:8201"
plugin_directory = "/openbao/plugins"

storage "file" {
  path = "/openbao/data"
}

listener "tcp" {
  address = "0.0.0.0:8200"
  tls_disable = 1
}

seal "static" {
  current_key_id = "e2e-local-1"
  current_key    = "file:///openbao/seal/static-unseal.key"
  disabled       = "false"
}
