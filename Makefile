.PHONY: test lint lint-fix integration-test integration-up integration-down

test:
	go test ./... -count=1

# Runs golangci-lint (preinstalled in the dev container; see .devcontainer/).
lint:
	golangci-lint run

lint-fix:
	golangci-lint run --fix

integration-up:
	docker compose -f integration/docker-compose.yml up -d --build --wait

integration-down:
	docker compose -f integration/docker-compose.yml down -v

integration-test: integration-up
	bash integration/run.sh; s=$$?; $(MAKE) integration-down; exit $$s
