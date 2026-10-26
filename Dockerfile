FROM --platform=$BUILDPLATFORM golang:1.24.3-alpine3.20 AS builder
ARG TARGETOS TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/external-dns-porkbun-webhook .

FROM alpine:3.24

COPY --from=builder /out/external-dns-porkbun-webhook /
USER 65532:65532
ENTRYPOINT ["/external-dns-porkbun-webhook"]
