# CFData-Next：基于 CFData-WEB 的多来源优选 + CFNext 推送
FROM golang:1.26-alpine AS build

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY index.html login.html favicon.png ca-certificates.crt ./

RUN go build -trimpath -ldflags="-s -w" -o /out/cfdata .

FROM alpine:3.21

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
