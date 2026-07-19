# Multi-stage build: static Go binaries on a distroless runtime.
FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/trader ./cmd/trader \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/backtester ./cmd/backtester \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/dashboard ./cmd/dashboard

# Distroless: no shell, CA certs + tzdata included (IANA zones required).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/trader /app/trader
COPY --from=build /out/backtester /app/backtester
COPY --from=build /out/dashboard /app/dashboard
COPY config/ /app/config/

EXPOSE 8443 8080
ENTRYPOINT ["/app/trader"]
CMD ["--config", "config/config.demo.yaml"]
# Cloud Run dashboard: docker run … --entrypoint /app/dashboard tradex --config config/config.demo.yaml
