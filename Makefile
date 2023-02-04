.PHONY: all build clean run check cover lint docker help
BIN_FILE=zoket-metric-adapter
all: check build
build:
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 @go build -o "${BIN_FILE}"
clean:
    @go clean
run:
    ./"${BIN_FILE}"
lint:
    golangci-lint run --enable-all
docker:
    @docker build -t localhost:5000/zoket-metric-adapter:latest .
help:
    @echo "make build"
    @echo "make clean"
    @echo "make run"
    @echo "make lint"
    @echo "make docker"
