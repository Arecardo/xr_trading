# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/market-info ./cmd/market-info \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/market-info-migrate ./cmd/market-info-migrate

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S marketinfo \
    && adduser -S -G marketinfo -H marketinfo

COPY --from=build /out/market-info /usr/local/bin/market-info
COPY --from=build /out/market-info-migrate /usr/local/bin/market-info-migrate

USER marketinfo
EXPOSE 8090
CMD ["/usr/local/bin/market-info", "serve"]
