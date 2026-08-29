# One image for every SwitchNet service. docker-compose.yml picks the binary
# with the container's command, so a rebuild is one build rather than five.

FROM golang:1.24-alpine AS build

WORKDIR /src

# Dependencies first, so a source change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 for a static binary: the runtime image has no libc to link
# against. -trimpath keeps the build directory out of the binary, so the same
# source produces the same bytes on any machine.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/dns        ./cmd/dns && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/dauth      ./cmd/dauth && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/aauth      ./cmd/aauth && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/baas       ./cmd/baas && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/stubs      ./cmd/stubs && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/natcheck   ./cmd/natcheck && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/npln       ./cmd/npln && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/dashboard  ./cmd/dashboard && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/switchnetctl ./cmd/switchnetctl

# Run the tests in the image build, so a broken commit cannot produce a
# deployable image.
RUN go vet ./... && go test ./...


FROM alpine:3.20

# ca-certificates is not needed to serve TLS (nginx terminates it) but is needed
# for any outbound HTTPS a future service makes. tzdata so log timestamps in a
# non-UTC deployment are readable.
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -H -u 10001 switchnet

COPY --from=build /out/ /usr/local/bin/

# The key directory and the image directory are volumes in docker-compose.yml.
# Creating them here with the right owner means the first run does not fail on a
# permission error as a non-root user.
RUN mkdir -p /var/lib/switchnet/keys /var/lib/switchnet/images && \
    chown -R switchnet /var/lib/switchnet && \
    chmod 700 /var/lib/switchnet/keys

# cap_net_bind_service so the DNS server can bind port 53 without running as
# root. A console cannot be told to use a different port, so 53 is not optional.
RUN apk add --no-cache libcap && \
    setcap cap_net_bind_service=+ep /usr/local/bin/dns && \
    apk del libcap

USER switchnet
WORKDIR /var/lib/switchnet

# Overridden per service in docker-compose.yml.
CMD ["/usr/local/bin/baas"]
