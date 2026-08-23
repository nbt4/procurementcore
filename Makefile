.PHONY: build test run frontend

frontend:
	cd web && npm ci && npm run build

build: frontend
	go build -o server ./cmd/server

test:
	go test ./...
	cd web && npm test

run:
	go run ./cmd/server
