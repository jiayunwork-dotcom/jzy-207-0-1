# Build stage: compile inside the Go 1.22 toolchain image.
FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /server ./cmd/server

# Runtime stage: minimal image with the binary only.
FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /server /server
COPY config.yaml /config.yaml
USER app
EXPOSE 8080
ENTRYPOINT ["/server", "-config", "/config.yaml"]
