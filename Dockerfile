# The default image: mark and a browser.
#
# mermaid is drawn by running mermaid.js in a real browser, and a d2 diagram's
# PNG is a screenshot of the SVG d2 drew. Both are what the binary does when it
# is told nothing, so this image sets no engine it has to be asked for -- the
# ENVs below only write the defaults down.
#
# There is a browserless counterpart in Dockerfile.nobrowser, which carries
# merman and resvg instead and is about a third of the size. It is the right
# image where nothing publishes maths, which still renders through Chrome and
# has no native path at all.

FROM golang:1.27.1 AS builder
ENV GOPATH="/go"
WORKDIR /go/src/github.com/kovetskiy/mark
COPY / .
RUN make get \
&& make build

FROM chromedp/headless-shell:latest

RUN apt-get update \
&& apt-get upgrade -qq \
&& apt-get install --no-install-recommends -qq ca-certificates bash sed dumb-init \
&& apt-get clean \
&& rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

COPY --from=builder /go/src/github.com/kovetskiy/mark/mark /bin/

# What this image draws with. Both are the binary's own defaults; they are here
# so that what an image renders with can be read off the image rather than
# inferred from what happens to be installed in it.
ENV MARK_MERMAID_ENGINE="chrome"
ENV MARK_D2_ENGINE="chrome"

WORKDIR /docs

ENTRYPOINT ["dumb-init", "--"]
