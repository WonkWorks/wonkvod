# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
COPY fixtures ./fixtures
COPY assets ./assets
ARG VCS_REF=unknown
ARG APP_VERSION=1.0.1
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.appVersion=${APP_VERSION} -X main.appRevision=${VCS_REF}" -o /out/wonkvod .

FROM alpine:3.22
ARG VCS_REF=unknown
ARG APP_VERSION=1.0.1
LABEL org.opencontainers.image.title="WonkVOD" \
      org.opencontainers.image.version="${APP_VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}"
RUN apk add --no-cache ca-certificates ffmpeg \
    && rm /usr/bin/ffmpeg
COPY --from=build /out/wonkvod /usr/local/bin/wonkvod
USER 65532:65532
EXPOSE 32499 32501
ENTRYPOINT ["/usr/local/bin/wonkvod"]
