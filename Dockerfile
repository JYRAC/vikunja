# Go製IPゲートをビルド
#   gate.go（ゲートと監視）と policy.go（許可リストの読み込み）を、Goモジュールとしてビルドする。
#   go.sum は、ビルド時に pgx の版から作る（Goのチェックサムデータベースで改ざんを検証）。
#   pgx の版を上げるときは、PGX_VERSION を書き換える。
FROM golang:1.24-alpine AS gate-build
# 依存の pgx が、より新しいGoを求める場合に、必要な版を自動で取得する
ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0
ARG PGX_VERSION=v5.9.2
WORKDIR /src
COPY gate.go policy.go ./
RUN go mod init gate && \
    go get github.com/jackc/pgx/v5@${PGX_VERSION} && \
    go mod tidy && \
    go build -trimpath -ldflags="-s -w" -o /out/gate .

# Vikunja本体
FROM vikunja/vikunja:2.7.1 AS vikunja

# OpenBao CLIを取得
FROM alpine:3.24 AS bao-download
ARG BAO_VERSION=2.7.0
RUN apk add --no-cache curl && \
    curl -fsSL -o /tmp/bao.tar.gz \
      "https://github.com/openbao/openbao/releases/download/v${BAO_VERSION}/openbao_${BAO_VERSION}_linux_amd64.tar.gz" && \
    mkdir -p /tmp/bao-bin && \
    tar -xzf /tmp/bao.tar.gz -C /tmp/bao-bin

# 最終イメージ
FROM alpine:3.24
RUN apk add --no-cache ca-certificates

COPY --from=vikunja /app/vikunja /app/vikunja
COPY --from=bao-download /tmp/bao-bin/bao /usr/local/bin/bao
COPY --from=gate-build /out/gate /app/gate

RUN chmod +x /usr/local/bin/bao /app/gate

ENV VIKUNJA_SERVICE_ROOTPATH=/app/vikunja/
WORKDIR /app/vikunja

COPY openbao/agent.hcl /etc/openbao/agent.hcl
COPY openbao/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 3456
ENTRYPOINT ["/entrypoint.sh"]
