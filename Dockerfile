# A self-contained build: docker build -t yumlab .
#
# Registry publishing is v0.5 in the roadmap. Until then this file is built by
# hand rather than by goreleaser, so it compiles the binary itself.

FROM golang:1.23-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not refetch them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=docker
# CGO off: the result must run in a scratch image with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /yumlab ./cmd/yumlab

FROM scratch

# Needed to reach the GitHub API over TLS; nothing else is required at runtime.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /yumlab /yumlab

# Workflow files are read from the working directory, so mount the repository
# there: docker run --rm -v "$PWD:/repo" -w /repo yumlab scan --offline
WORKDIR /repo

ENTRYPOINT ["/yumlab"]
CMD ["scan"]
