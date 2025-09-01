.PHONY: build run tidy clean

build:
	@mkdir -p build
	GO111MODULE=on go build -o build/tacnet-odenwakun ./src

run:
	GO111MODULE=on go run ./src

tidy:
	go mod tidy

clean:
	rm -rf build
