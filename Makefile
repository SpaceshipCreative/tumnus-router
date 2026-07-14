.PHONY: build test clean

build:
	go build -buildmode=plugin -o tumnus.so .

test:
	go test ./...

clean:
	rm -f tumnus.so
