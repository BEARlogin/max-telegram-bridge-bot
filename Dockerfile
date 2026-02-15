FROM golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=1 go build -o /max-telegram-bridge-bot .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates
RUN adduser -D -h /app bridge
USER bridge
WORKDIR /app

COPY --from=builder /max-telegram-bridge-bot /usr/local/bin/max-telegram-bridge-bot

ENTRYPOINT ["max-telegram-bridge-bot"]
