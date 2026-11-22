# The satelle node image: a plain OCI image built from this repo's source, so
# the same node runs on any container host. `satelle remote fire` builds it as
# satelle-node:local for a docker-local remote that names no [machine] image.
# The Makefile stamps .version; .dockerignore keeps .git out, so the binary
# carries the plain base version.
FROM golang:1.27-alpine AS build
RUN apk add --no-cache make bash
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 make build

FROM alpine:3
RUN apk add --no-cache git ca-certificates
COPY --from=build /src/satelle /usr/local/bin/satelle
