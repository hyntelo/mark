# This image has no browser in it. Upstream's is built on chromedp/headless-shell,
# because mermaid is drawn by running mermaid.js in a real browser and the SVG that
# d2 produces is turned into a PNG by taking a picture of it. That is about 280 MB
# of Chrome, plus everything it needs.
#
# Neither job needs a browser any more:
#
#   * mermaid  -> merman-cli, a native reimplementation, selected at run time with
#                 --mermaid-engine=merman (MARK_MERMAID_ENGINE). Upstream already
#                 installs it; this image just has nothing to fall back to, so the
#                 flag is not optional here.
#   * d2       -> resvg. d2 compiles, lays out and renders the SVG on its own in
#                 Go; Chrome was only the rasteriser. See d2/d2.go.
#
# The maths feature (--features=math) still renders through Chrome upstream, so it
# will not work in this image. Nothing we publish uses it.

# ---------------------------------------------------------------------------
# Stage 1: the mark binary, and d2's fonts
# ---------------------------------------------------------------------------
FROM golang:1.27.1 AS builder
ENV GOPATH="/go"
WORKDIR /go/src/github.com/kovetskiy/mark
COPY / .
RUN make get \
&& make build \
# d2 embeds a subset of Source Sans Pro / Source Code Pro into every SVG it draws,
# as base64 @font-face rules that resvg cannot read (see substituteEmbeddedFonts in
# d2/d2.go). We take the very same .ttf files out of d2's Go module and install them
# below, so the glyphs resvg draws are the ones d2 measured the text with -- a
# different copy of "the same" font has different metrics, and the layout d2
# computed would no longer fit.
&& mkdir -p /d2-fonts \
&& cp "$(go env GOMODCACHE)"/github.com/d2lang/d2@*/d2renderers/d2fonts/ttf/*.ttf /d2-fonts/

# ---------------------------------------------------------------------------
# Stage 2: resvg
# ---------------------------------------------------------------------------
# resvg publishes no prebuilt Linux binaries, so it has to be compiled.
FROM rust:1-slim-trixie AS resvg-builder
ARG RESVG_VERSION=0.48.1
RUN cargo install --locked resvg --version ${RESVG_VERSION}

# ---------------------------------------------------------------------------
# Stage 3: the final image
# ---------------------------------------------------------------------------
FROM debian:trixie-slim

ARG TARGETARCH

RUN apt-get update \
&& apt-get upgrade -qq \
&& apt-get install --no-install-recommends -qq \
     ca-certificates bash sed python3 dumb-init \
     fonts-liberation2 fonts-dejavu-core fontconfig \
     curl xz-utils \
&& apt-get clean \
&& rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

# The version mark itself requires, rather than one written down again here: the
# same file is embedded in the binary, which refuses a merman older than it. A
# version kept in two places is a version updated in one, and the failure would be
# a published diagram rather than a build that stopped. Copied after the apt step
# above, which clears /tmp.
COPY --from=builder /go/src/github.com/kovetskiy/mark/mermaid/merman-version.txt /etc/merman-version

# amd64 only, which is where merman publishes a Linux build. Upstream's image falls
# back to Chrome on other architectures; this one has no fallback, so an arm64 build
# would draw no mermaid at all. The Azure pool is amd64.
RUN set -eux; \
    if [ "${TARGETARCH}" = "amd64" ]; then \
        MERMAN_VERSION="$(cat /etc/merman-version)"; \
        base="https://github.com/Latias94/merman/releases/download/v${MERMAN_VERSION}"; \
        archive="merman-cli-x86_64-unknown-linux-gnu.tar.xz"; \
        cd /tmp; \
        curl -fsSL -o "${archive}" "${base}/${archive}"; \
        curl -fsSL -o "${archive}.sha256" "${base}/${archive}.sha256"; \
        sha256sum -c "${archive}.sha256"; \
        tar -xJf "${archive}"; \
        install -m 0755 merman-cli-x86_64-unknown-linux-gnu/merman-cli /usr/local/bin/merman-cli; \
        merman-cli --version; \
        rm -rf /tmp/merman-cli-*; \
    fi

# curl and xz-utils were only needed for the download above.
RUN apt-get purge -y -qq curl xz-utils \
&& apt-get autoremove -y -qq \
&& rm -rf /var/lib/apt/lists/*

COPY --from=builder /d2-fonts/ /usr/share/fonts/truetype/d2/
COPY --from=resvg-builder /usr/local/cargo/bin/resvg /usr/local/bin/resvg
COPY --from=builder /go/src/github.com/kovetskiy/mark/mark /bin/

# Builds the font index. Without it fontconfig finds nothing in the directory we
# just filled, and we are back to diagrams with no text.
RUN fc-cache -f

WORKDIR /docs

ENTRYPOINT ["dumb-init", "--"]
