all: test build

test :
	go test -v ./...

build :
	mkdir bin || true
	go build -x -trimpath -o ./bin/goloc cmd/goloc/*.go

install :
	go install -x ./cmd/goloc/
