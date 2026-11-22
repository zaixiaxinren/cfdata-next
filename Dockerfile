# CFData-Next：基于 CFData-WEB 的多来源优选 + CFNext 推送
#
# 支持多架构：buildx 在构建机原生架构上交叉编译，
# 不需要 QEMU 模拟，arm64 也能在几秒内编完。
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY index.html login.html favicon.png ca-certificates.crt ./

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/cfdata .

# 单元测试阶段（可选）：docker build --target test .
FROM build AS test
RUN go test -v ./...

FROM alpine:3.21

ARG TARGETARCH
LABEL org.opencontainers.image.title="cfdata-next" \
      org.opencontainers.image.description="CFData-WEB 的 Docker 化版本：多来源优选、容器内定时测速、结果推送到 CFNext 面板" \
      org.opencontainers.image.source="https://github.com/zaixiaxinren/cfdata-next" \
      org.opencontainers.image.licenses="GPL-3.0"

RUN apk add --no-cache ca-certificates tzdata && \
    mkdir -p /data

WORKDIR /data
COPY --from=build /out/cfdata /app/cfdata

ENV CFDATA_DATA_DIR=/data \
    TZ=Asia/Shanghai

EXPOSE 13335
VOLUME ["/data"]

ENTRYPOINT ["/app/cfdata"]
CMD ["-host", "0.0.0.0", "-port", "13335"]
