.PHONY: build test clean

build:
	go build -buildmode=plugin -o suprarouter.so .

test:
	go test ./...

clean:
	rm -f suprarouter.so
