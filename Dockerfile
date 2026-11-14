# ytx serve as a small container: one static binary, no shell, non-root.
# Build from a pinned tag, stamping it as the version:
#   docker build --build-arg VERSION=v1.2.3 -t ytx:v1.2.3 .
# It has no authentication: run it on a private network only your own backend
# can reach, and do not publish its port to the internet.
ARG GO_VERSION=1.27
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/morethancoder/ytx/internal/cli.Version=${VERSION}" \
    -o /out/ytx ./cmd/ytx

# distroless/static carries CA certificates (needed for HTTPS to YouTube) and a
# non-root user, and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ytx /ytx
USER nonroot:nonroot
EXPOSE 8787
# Listen on every interface of the container; the network around it keeps it private.
ENV YTX_SERVE_ADDR=0.0.0.0:8787
ENTRYPOINT ["/ytx", "serve"]
