FROM golang:1.27.1 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/service ./cmd/consumer

FROM scratch
COPY --from=builder /out/service /service
ENTRYPOINT ["/service"]
