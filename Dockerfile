FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o mc-discord .

FROM alpine:3.21

RUN adduser -D -H appuser
COPY --from=builder /src/mc-discord /usr/local/bin/mc-discord

USER appuser
ENTRYPOINT ["mc-discord"]
