# 构建阶段
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# 先复制依赖文件，利用 Docker 缓存层
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# 复制源代码并构建
COPY backend/ .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /linguaflow ./cmd/linguaflow

# 运行阶段 - 使用精简镜像
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

# 创建非 root 用户
RUN adduser -D -g '' appuser

# 创建数据目录并设置权限
RUN mkdir -p /app/data && chown appuser:appuser /app/data

COPY --from=builder /linguaflow /usr/local/bin/linguaflow

USER appuser

# 设置工作目录
WORKDIR /app

EXPOSE 8080

ENTRYPOINT ["linguaflow"]
CMD ["serve"]
