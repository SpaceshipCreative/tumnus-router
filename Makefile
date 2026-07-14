CORE_VERSION ?=

.PHONY: build test clean pin

pin:
ifneq ($(CORE_VERSION),)
	go get github.com/maximhq/bifrost/core@$(CORE_VERSION) && go mod tidy
endif

build: pin
	go build -buildmode=plugin -o tumnus.so .
	go build -o tumnus-dashboard ./cmd/dashboard

test:
	go test -race ./...

clean:
	rm -f tumnus.so tumnus-dashboard
