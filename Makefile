.PHONY: build run clean docker-build docker-run docker-push vet

build:
	go build -o bin/relay ./cmd/relay

run: build
	./bin/relay

vet:
	go vet ./...

clean:
	rm -rf bin/

docker-build:
	docker build -t sesori-relay .

docker-run: docker-build
	docker run -d -p 8080:8080 --name relay sesori-relay

docker-push:
	@echo "Set REGISTRY and run: docker push $${REGISTRY}/sesori-relay"
