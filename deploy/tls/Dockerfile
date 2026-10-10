# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY deploy/tls ./deploy/tls
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/wonkvod-tls ./deploy/tls

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/wonkvod-tls /usr/local/bin/wonkvod-tls
USER 1000:1000
EXPOSE 32499
ENTRYPOINT ["/usr/local/bin/wonkvod-tls"]
