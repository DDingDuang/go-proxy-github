.PHONY: build run test vet clean

BIN := bin/github-gateway

build:
	go build -o $(BIN) .

run: build
	$(BIN) -config config.yaml

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin logs
