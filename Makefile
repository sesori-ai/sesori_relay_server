.PHONY: build-relay build-bridge build-all run-relay run-bridge clean docker-build docker-run docker-push

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

docker-build:
	docker build -t remote-relay .

docker-run: docker-build
	docker run -d -p 8080:8080 --name relay remote-relay

docker-push:
	@echo "Set REGISTRY and run: docker push $${REGISTRY}/remote-relay"
