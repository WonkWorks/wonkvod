# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
COPY fixtures ./fixtures
ARG VCS_REF=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/wonkvod .

FROM alpine:3.22
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="WonkVOD" \
      org.opencontainers.image.revision="${VCS_REF}"
RUN apk add --no-cache ca-certificates
COPY --from=build /out/wonkvod /usr/local/bin/wonkvod
USER 65532:65532
EXPOSE 32499
ENTRYPOINT ["/usr/local/bin/wonkvod"]
