FROM golang:1.16.3-alpine3.13 as builder
ARG VERSION

WORKDIR /src
COPY go.mod go.sum ./
COPY pkg/transflect/ ./pkg/transflect/
COPY cmd/transflect-operator/ ./cmd/transflect-operator/
COPY cmd/transflect/ ./cmd/transflect/
RUN go build -ldflags="-X main.version=${VERSION}" ./cmd/transflect-operator
RUN go build -ldflags="-X main.version=${VERSION}" ./cmd/transflect

FROM alpine:3.13
RUN apk --no-cache add curl
COPY --from=builder /src/transflect-operator /app/
COPY --from=builder /src/transflect /app/

# Set to a non-root user, outside the standard range of commonly set user ids
RUN addgroup -S -g 2000 appuser && adduser -S -u 2000 -g appuser appuser
USER appuser

ENTRYPOINT /app/transflect-operator --plaintext --log-format json
