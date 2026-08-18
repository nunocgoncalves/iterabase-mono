# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/nunocgoncalves/iterabase-mono/inference-gateway/internal/version.version=${VERSION} -X github.com/nunocgoncalves/iterabase-mono/inference-gateway/internal/version.commit=${COMMIT} -X github.com/nunocgoncalves/iterabase-mono/inference-gateway/internal/version.date=${DATE}" \
    -o /gateway ./cmd/gateway

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /gateway /usr/local/bin/gateway

EXPOSE 8080 9090

ENTRYPOINT ["gateway"]
CMD ["serve"]
