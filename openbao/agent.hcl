vault {
  address = "https://openbao.jyrac.stki.org"
}

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

exec {
  command = ["/app/gate"]
  restart_on_secret_changes = "always"
}
