FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o controller ./cmd/controller

FROM alpine:3.19

# Install tailscale CLI so the controller can run `tailscale serve --config`
RUN apk add --no-cache tailscale ca-certificates

COPY --from=builder /app/controller /usr/local/bin/controller

ENTRYPOINT ["/usr/local/bin/controller"]
