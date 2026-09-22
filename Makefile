NAME = $(notdir $(PWD))

VERSION = $(shell git describe --tags --abbrev=0)
COMMIT = $(shell git rev-parse HEAD)
GO111MODULE = on

version:
	@echo $(VERSION)

get:
	go get -v -d

build:
	@echo :: building go binary $(VERSION)
	CGO_ENABLED=0 go build \
		-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-gcflags "-trimpath $(GOPATH)/src" \
		-o $(NAME) \
		./cmd/mark

test:
	go test -race -coverprofile=profile.cov ./... -v

# The image with a browser in it. Dockerfile.nobrowser is the other one, which
# carries merman and resvg instead; neither is published anywhere, so both are
# built by hand when somebody wants a container.
image:
	@echo :: building image $(NAME):$(VERSION)
	@docker build -t $(NAME):$(VERSION) -f Dockerfile .
	docker tag $(NAME):$(VERSION) $(NAME):latest

clean:
	rm -rf $(NAME)
