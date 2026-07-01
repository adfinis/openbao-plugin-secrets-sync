ui = false
log_level = "info"

api_addr = "http://127.0.0.1:8200"
cluster_addr = "http://openbao-standby:8201"
disable_standby_reads = false
enable_response_header_raft_node_id = true
plugin_directory = "/openbao/plugins"

storage "raft" {
  path = "/openbao/data"
  node_id = "openbao-node1"
  performance_multiplier = 1

  retry_join {
    leader_api_addr = "http://openbao:8200"
  }

  retry_join {
    leader_api_addr = "http://openbao-standby:8200"
  }

  retry_join {
    leader_api_addr = "http://openbao-standby-2:8200"
  }
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
