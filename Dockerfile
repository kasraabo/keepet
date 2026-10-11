FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# A cloud proxy CA can be supplied only at build time; verification remains enabled.
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then export SSL_CERT_FILE=/run/secrets/build_ca; fi; \
    go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /keepet ./cmd/server
FROM alpine:3.22
RUN adduser -D -u 10001 keepet
USER keepet
COPY --from=build /keepet /usr/local/bin/keepet
EXPOSE 8080
ENTRYPOINT ["keepet"]
