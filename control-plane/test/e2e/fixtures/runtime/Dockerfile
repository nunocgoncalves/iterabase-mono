# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/e2e-runtime ./main.go

FROM alpine:3.22
RUN apk add --no-cache ca-certificates git openssh
COPY --from=builder /out/e2e-runtime /usr/local/bin/e2e-runtime
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/e2e-runtime"]
