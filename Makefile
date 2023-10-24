.PHONY: all build clean run check cover lint docker help
VERSION=`git describe --always --dirty`
BIN_FILE=zoekt-metrics-adapter
all: check build
build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-X github.com/google/zoekt.Version=${VERSION}" -o "${BIN_FILE}"
clean:
	go clean
run:
	./"${BIN_FILE}"
lint:
	golangci-lint run --enable-all
docker:
	docker build -t localhost:5000/zoket-metric-adapter:latest .
help:
	echo "make build"
	echo "make clean"
	echo "make run"
	echo "make lint"
	echo "make docker"
