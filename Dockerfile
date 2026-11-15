
# Go製IPゲートをビルド
#   gate.go（ゲートと監視）と policy.go（許可リストの読み込み）を、
#   Goモジュールとしてビルドする。
#   go.sum はビルド時に pgx の版から作る。
#   pgx の版を上げるときは PGX_VERSION を書き換える。
FROM golang:1.27-alpine AS gate-build

ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0

ARG PGX_VERSION=v5.9.2

WORKDIR /src
COPY gate.go policy.go ./

RUN go mod init gate && \
    go get github.com/jackc/pgx/v5@${PGX_VERSION} && \
    go mod tidy && \
    go build -trimpath -ldflags="-s -w" -o /out/gate .


# Casdoor（SSO）への、NetBird 経由の TCP 中継をビルド（標準ライブラリのみ）
FROM golang:1.27-alpine AS nbtcp-build

ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0

WORKDIR /src
COPY nbtcp.go ./
RUN go mod init nbtcp && \
    go build -trimpath -ldflags="-s -w" -o /out/nbtcp .


# Vikunja本体
FROM vikunja/vikunja:2.8.0 AS vikunja


# OpenBao CLIを取得
FROM alpine:3.24 AS bao-download

ARG BAO_VERSION=2.7.2

RUN apk add --no-cache curl && \
    curl -fsSL -o /tmp/bao.tar.gz \
      "https://github.com/openbao/openbao/releases/download/v${BAO_VERSION}/openbao_${BAO_VERSION}_linux_amd64.tar.gz" && \
    mkdir -p /tmp/bao-bin && \
    tar -xzf /tmp/bao.tar.gz -C /tmp/bao-bin


# NetBird クライアントを取得
FROM alpine:3.24 AS netbird-download

ARG NETBIRD_VERSION=0.81.5

RUN apk add --no-cache curl && \
    curl -fsSL -o /tmp/netbird.tar.gz \
      "https://github.com/netbirdio/netbird/releases/download/v${NETBIRD_VERSION}/netbird_${NETBIRD_VERSION}_linux_amd64.tar.gz" && \
    mkdir -p /tmp/netbird-bin && \
    tar -xzf /tmp/netbird.tar.gz -C /tmp/netbird-bin netbird


# 最終イメージ
FROM alpine:3.24

RUN apk add --no-cache ca-certificates

COPY --from=vikunja /app/vikunja /app/vikunja
COPY --from=bao-download /tmp/bao-bin/bao /usr/local/bin/bao
COPY --from=netbird-download /tmp/netbird-bin/netbird /usr/local/bin/netbird
COPY --from=gate-build /out/gate /app/gate
COPY --from=nbtcp-build /out/nbtcp /usr/local/bin/nbtcp

# NetBird の実行確認
RUN chmod +x /usr/local/bin/bao /usr/local/bin/netbird /app/gate /usr/local/bin/nbtcp && \
    /usr/local/bin/netbird version

ENV VIKUNJA_SERVICE_ROOTPATH=/app/vikunja/

WORKDIR /app/vikunja

# OpenBao の NetBird 向け証明書
# openbao/ca.crt はリポジトリに配置する。
COPY openbao/ca.crt /etc/openbao/ca.crt
# Vikunja が、Casdoor の内部口の証明書を検証するための、信頼する証明書。
# 通常の証明書（ca-certificates）に、ルート入りのバンドル（ca.crt）を足したもの。
# ルートは、名前制約つき（netbird.cloud と jyrac.stki.org のみ）なので、他の名前には使えない。
# Vikunja の実行時に、SSL_CERT_FILE で指す（中継を使うときだけ。entrypoint.sh）。
RUN tr -d '\r' < /etc/openbao/ca.crt > /tmp/ca.lf && \
    cat /etc/ssl/certs/ca-certificates.crt /tmp/ca.lf > /etc/openbao/vikunja-trust.crt && \
    rm /tmp/ca.lf
COPY openbao/agent.hcl /etc/openbao/agent.hcl
COPY openbao/entrypoint.sh /entrypoint.sh
COPY openbao/netbird-up.sh /netbird-up.sh

RUN sed -i 's/\r$//' /entrypoint.sh /netbird-up.sh && \
    chmod +x /entrypoint.sh /netbird-up.sh

EXPOSE 3456

ENTRYPOINT ["/entrypoint.sh"]