# Build stage: compile inside the Go 1.22 toolchain image.
FROM golang:1.22-alpine AS build
WORKDIR /src

# Dependencies first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yardstress ./cmd/server

# Runtime stage: minimal image with just the binary and the demo config.
FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/yardstress /usr/local/bin/yardstress
COPY config.yaml /etc/yardstress/config.yaml
USER app
ENV CONFIG_PATH=/etc/yardstress/config.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/yardstress"]
