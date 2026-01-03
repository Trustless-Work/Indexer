build:
	go build -o bin/ingest cmd/ingest.go

run: build
	./bin/ingest