.PHONY: run build test vet tidy migrate-up migrate-down

run:
	PORT=8080 \
	ENV=dev \
	JWT_SECRET=demo-secret-for-testing \
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

migrate-up:
	migrate -path migrations -database "$(shell python3 -c \"import os; print('file://' + os.path.expanduser(os.environ.get('DB_PATH', './data/tasks.db')))\" 2>/dev/null || echo 'file://./data/tasks.db')" up

migrate-down:
	migrate -path migrations -database "$(shell python3 -c \"import os; print('file://' + os.path.expanduser(os.environ.get('DB_PATH', './data/tasks.db')))\" 2>/dev/null || echo 'file://./data/tasks.db')" down
