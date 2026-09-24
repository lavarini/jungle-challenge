# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wagerd ./cmd/wagerd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wagerd /wagerd
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/wagerd"]
