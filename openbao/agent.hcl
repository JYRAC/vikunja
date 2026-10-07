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

exec {
  command                   = ["/app/vikunja/vikunja"]
  restart_on_secret_changes = "always"
}