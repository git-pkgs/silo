FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /silo ./cmd/silo

FROM gcr.io/distroless/static
COPY --from=build /silo /silo
USER nonroot
VOLUME /data
EXPOSE 8080 2222
ENTRYPOINT ["/silo", "serve", "--data", "/data"]
