FROM golang:1.27.1 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/lab-server .

FROM scratch
COPY --from=build --chmod=0555 /out/lab-server /lab-server
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 1000:1000
ENTRYPOINT ["/lab-server"]
