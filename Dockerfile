FROM golang:1.27-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /lbplane ./cmd/lbplane

# Needs the haproxy binary for config checks.
FROM haproxy:3.0
COPY --from=build /lbplane /usr/local/bin/lbplane
USER root
ENTRYPOINT ["lbplane"]
