FROM golang:1.26.5-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/tdc-telemetry-backend ./cmd/tdc-telemetry-backend
COPY cmd/tdc-telemetry-migrate ./cmd/tdc-telemetry-migrate
COPY internal/telemetrybackend ./internal/telemetrybackend
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/tdc-telemetry-backend ./cmd/tdc-telemetry-backend
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/tdc-telemetry-migrate ./cmd/tdc-telemetry-migrate

FROM alpine:3.23

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S -g 10001 telemetry \
    && adduser -S -D -H -u 10001 -G telemetry telemetry
COPY --from=build /out/tdc-telemetry-backend /usr/local/bin/tdc-telemetry-backend
COPY --from=build /out/tdc-telemetry-migrate /usr/local/bin/tdc-telemetry-migrate

USER telemetry
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tdc-telemetry-backend"]
