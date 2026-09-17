# syntax=docker/dockerfile:1
# One-shot acceptance image: the official Playwright image already contains
# Node 20, Chromium and every shared library Chromium needs; we only add the
# Go toolchain so the same container can run the Go test suite too.
FROM mcr.microsoft.com/playwright:v1.48.0-noble

# dpkg --print-architecture yields arm64 / amd64, which matches Go's tarball
# names, so the build works on both platforms without extra build args.
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl ca-certificates tar \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && curl -fsSL "https://go.dev/dl/go1.23.4.linux-${arch}.tar.gz" \
       | tar -xz -C /usr/local
ENV PATH="/usr/local/go/bin:/tmp/go/bin:${PATH}" \
    GOCACHE=/tmp/go-build \
    GOPATH=/tmp/go \
    GOFLAGS=-buildvcs=false

WORKDIR /workspace
CMD ["bash", "/workspace/verify/entrypoint.sh"]
