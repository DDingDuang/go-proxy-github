.PHONY: build test vet clean

build:
	./scripts/build.sh

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin dist logs
