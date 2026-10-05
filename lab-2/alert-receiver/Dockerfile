FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/alert-receiver .

FROM scratch
COPY --from=build --chmod=0555 /out/alert-receiver /alert-receiver
USER 1000:1000
ENTRYPOINT ["/alert-receiver"]
