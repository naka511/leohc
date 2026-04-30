FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o leo-go ./cmd/server/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/leo-go .
COPY --from=builder /build/static ./static
COPY --from=builder /build/config ./config
EXPOSE 8787
CMD ["./leo-go"]
