# 构建阶段
FROM golang:1.26-alpine AS builder
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o /out/github-gateway .

# 运行阶段
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/github-gateway /usr/local/bin/github-gateway
COPY config.yaml /app/config.yaml
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/github-gateway", "-config", "/app/config.yaml"]
