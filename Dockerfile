FROM golang:1.22-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates && update-ca-certificates

# モジュール分離でキャッシュを効かせる
COPY go.mod go.sum ./
RUN go mod download

COPY ./src ./src

# 静的リンク＆最適化
ENV CGO_ENABLED=0
RUN go build -ldflags="-s -w" -o /out/tacnet-odenwakun ./src

FROM alpine:3.19
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata && update-ca-certificates
COPY --from=build /out/tacnet-odenwakun /app/tacnet-odenwakun

ENTRYPOINT ["/app/tacnet-odenwakun"]
