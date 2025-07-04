FROM golang:1.23.6-alpine3.20 AS builder
WORKDIR /app
COPY . /app

RUN apk --no-cache add make git && make build

FROM alpine:3.22

COPY --from=builder /app/external-dns-porkbun-webhook /
ENTRYPOINT ["/external-dns-porkbun-webhook"]
