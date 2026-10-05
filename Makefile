.PHONY: up down reset logs psql seed test load proto proto-deps proto-lint

# k6 script to run with `make load` (baseline, ramp, batch or soak) and extra k6 arguments
SCRIPT ?= baseline
K6_ARGS ?=

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

# Load 20,000 runs and 1,000,000 results into the app database
seed:
	docker compose exec -T postgres psql -U results -f - < test/load/seed.sql

# Run all Go test layers in a container, against the test database
test:
	docker compose --profile test run --rm tests

# Run a k6 script against the running stack, e.g. make load SCRIPT=ramp K6_ARGS="-e PROTOCOL=grpc"
load:
	docker compose --profile load run --rm k6 run $(K6_ARGS) /scripts/$(SCRIPT).js

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
