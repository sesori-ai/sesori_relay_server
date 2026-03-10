.PHONY: build-relay build-bridge build-all run-relay run-bridge clean

build-relay:
	go build -o bin/relay ./cmd/relay

build-bridge:
	go build -o bin/bridge ./cmd/bridge

build-all: build-relay build-bridge

run-relay: build-relay
	./bin/relay

run-bridge: build-bridge
	./bin/bridge

clean:
	rm -rf bin/
