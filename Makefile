.PHONY: up down reset logs psql test load proto proto-deps proto-lint

# Build images and start the stack in the background
up:
	docker compose up -d --build

# Stop the stack, keep the database volume
down:
	docker compose down

# Stop the stack and delete the database volume
reset:
	docker compose down -v

# Follow server logs
logs:
	docker compose logs -f server

# SQL shell on the app database
psql:
	docker compose exec postgres psql -U results

# Run all Go test layers in a container (needs the test profile services, milestone 3+)
test:
	docker compose --profile test run --rm tests

# Run the k6 baseline against the running stack (needs the load profile service, milestone 8)
load:
	docker compose --profile load run --rm k6

# Resolve proto dependencies (googleapis) and write buf.lock
proto-deps:
	buf dep update

# Lint and format-check the proto files
proto-lint:
	buf lint
	buf format --diff --exit-code

# Generate Go code from the proto files into gen/
proto:
	buf generate
