FROM node:22-alpine AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/cmd/server/dist ./cmd/server/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o procurementcore ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget && addgroup -S app && adduser -S app -G app && mkdir -p /var/lib/branding/logos /app && chown -R app:app /var/lib/branding /app
WORKDIR /app
COPY --from=builder --chown=app:app /app/procurementcore ./procurementcore
COPY --from=builder --chown=app:app /app/migrations ./migrations
USER app
EXPOSE 8084
HEALTHCHECK --interval=30s --timeout=10s --start-period=20s --retries=3 CMD wget -qO- http://localhost:8084/health || exit 1
CMD ["./procurementcore"]
