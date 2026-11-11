# vault ブロック（接続先、証明書）は、entrypoint.sh が、経路に合わせて作って、この前に付ける。

auto_auth {
  method {
    type = "token_file"
    config = {
      token_file_path = "/tmp/vault-token"
    }
  }
}

env_template "VIKUNJA_DATABASE_USER" {
  contents = "{{ with secret \"database/creds/vikunja\" }}{{ .Data.username }}.kuajmlczxracqobxkzee{{ end }}"
}

env_template "VIKUNJA_DATABASE_PASSWORD" {
  contents = "{{ with secret \"database/creds/vikunja\" }}{{ .Data.password }}{{ end }}"
}

env_template "VIKUNJA_AUTH_OPENID_PROVIDERS_CASDOOR_CLIENTSECRET" {
  contents = "{{ with secret \"kv/data/secrets/vikunja/config\" }}{{ .Data.data.casdoor_client_secret }}{{ end }}"
}

# ゲート専用の、読み取りだけのDB資格情報（Vikunja本体のロールとは別）。
# GATE_ で始まる環境変数は、ゲートが Vikunja の子プロセスには渡さない。
env_template "GATE_DB_USER" {
  contents = "{{ with secret \"database/creds/gate-reader\" }}{{ .Data.username }}.kuajmlczxracqobxkzee{{ end }}"
}

env_template "GATE_DB_PASSWORD" {
  contents = "{{ with secret \"database/creds/gate-reader\" }}{{ .Data.password }}{{ end }}"
}

# ゲートは、`env -u` を通して起動する。Agent が OpenBao に出るための設定（SOCKS5 のプロキシ、
# OpenBao 向けの BAO_*）を、ゲートと Vikunja に渡さないため。渡すと、Vikunja の外向きの通信
# （Casdoor、Supabase など）まで、NetBird の SOCKS5 に流れて、失敗する。
exec {
  command = [
    "/usr/bin/env",
    "-u", "HTTPS_PROXY", "-u", "HTTP_PROXY", "-u", "NO_PROXY",
    "-u", "https_proxy", "-u", "http_proxy", "-u", "no_proxy",
    "-u", "BAO_ADDR", "-u", "BAO_CACERT", "-u", "BAO_TLS_SERVER_NAME", "-u", "BAO_CLIENT_TIMEOUT",
    "/app/gate"
  ]
  restart_on_secret_changes = "always"
}
