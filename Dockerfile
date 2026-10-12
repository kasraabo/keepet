FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY . .
# Dependencies are checked against go.sum before refreshing the committed vendor
# snapshot. Compile without network access, including on restricted cloud hosts.
RUN --network=none CGO_ENABLED=0 GOPROXY=off go build -mod=vendor -trimpath -o /keepet ./cmd/server
FROM alpine:3.22
RUN adduser -D -u 10001 keepet && mkdir -p /var/lib/keepet && chown keepet:keepet /var/lib/keepet
USER keepet
COPY --from=build /keepet /usr/local/bin/keepet
EXPOSE 8080
ENTRYPOINT ["keepet"]
