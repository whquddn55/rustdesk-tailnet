# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/rustdesk-tailnet-api . \
 && mkdir -p /out/data

# distroless/static: no shell, CA certs + tzdata only, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rustdesk-tailnet-api /usr/local/bin/rustdesk-tailnet-api
COPY --from=build --chown=65532:65532 /out/data /data
ENV DB_PATH=/data/api.db
VOLUME ["/data"]
EXPOSE 21114
ENTRYPOINT ["/usr/local/bin/rustdesk-tailnet-api"]
CMD ["serve"]
