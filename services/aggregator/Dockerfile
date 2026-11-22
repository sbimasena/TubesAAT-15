FROM golang:1.27.1 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/service ./cmd/server

FROM scratch
# The satellite feed is public HTTPS; scratch has no CA bundle to verify it.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/service /service
ENTRYPOINT ["/service"]
