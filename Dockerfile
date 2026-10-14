# Go製IP観測ゲートをビルド
FROM golang:1.24-alpine AS gate-build
WORKDIR /src
COPY gate.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gate gate.go

# Vikunja本体
FROM vikunja/vikunja:2.6.0 AS vikunja

# OpenBao CLIを取得
FROM alpine:3.19 AS bao-download
ARG BAO_VERSION=2.7.0
RUN apk add --no-cache curl && \
    curl -fsSL -o /tmp/bao.tar.gz \
      "https://github.com/openbao/openbao/releases/download/v${BAO_VERSION}/openbao_${BAO_VERSION}_linux_amd64.tar.gz" && \
    mkdir -p /tmp/bao-bin && \
    tar -xzf /tmp/bao.tar.gz -C /tmp/bao-bin

# 最終イメージ
FROM alpine:3.19
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

# 公開HTTP入口はゲートが担当
EXPOSE 10000

ENTRYPOINT ["/entrypoint.sh"]