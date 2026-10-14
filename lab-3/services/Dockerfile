FROM golang:1.26.1-alpine3.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG SERVICE
ARG APP_VERSION=0.1.0
RUN case "$SERVICE" in api|worker) ;; *) echo 'SERVICE must be api or worker' >&2; exit 1 ;; esac \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
       -ldflags="-s -w -X lab3/services/internal/app.Version=${APP_VERSION}" \
       -o /out/service "./cmd/${SERVICE}"

FROM alpine:3.23
RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app
COPY --from=build /out/service /usr/local/bin/service
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/service"]
