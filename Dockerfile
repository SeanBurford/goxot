FROM alpine:3.24 as build

env GOPATH=/app/build/

RUN addgroup -g 10001 -S app && \
    adduser -u 10001 -S app -G app

RUN apk update && apk add alpine-sdk go

WORKDIR /app

COPY --chown=app:app go.mod .
COPY --chown=app:app src/ ./src/

USER app

RUN go install ./src/cmd/...

FROM alpine:3.24 as runner

RUN addgroup -g 10001 -S app && \
    adduser -u 10001 -S app -G app

WORKDIR /app

USER app

copy --from=build /app/build/bin/ .

CMD ["/app/tun-gateway", "--help"]
