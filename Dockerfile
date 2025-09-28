# 构建阶段
FROM golang:1.21-alpine AS builder

# 安装构建依赖
RUN apk add --no-cache git gcc musl-dev

# 设置工作目录
WORKDIR /app

# 复制go mod文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建应用
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o scum_run_$(date +%s).exe .

# 运行时镜像
FROM alpine:latest AS runtime

# 安装运行时依赖
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    sqlite \
    curl

# 设置时区
ENV TZ=Asia/Shanghai

# 创建非root用户
RUN addgroup -g 1001 appgroup && \
    adduser -u 1001 -G appgroup -D appuser

# 设置工作目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /app/scum_run .

# 创建必要的目录
RUN mkdir -p /app/data \
             /app/logs \
             /app/config && \
    chown -R appuser:appgroup /app

# 切换到非root用户
USER appuser

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD pgrep scum_run || exit 1

# 启动命令
CMD ["./scum_run"] 