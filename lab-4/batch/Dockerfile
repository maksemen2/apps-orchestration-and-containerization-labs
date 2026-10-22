FROM golang:1.26.1-alpine3.23 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/batch .

FROM scratch
COPY --from=build /out/batch /batch
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/batch"]
