.PHONY: test integration-test integration-up integration-down

test:
	go test ./... -count=1

integration-up:
	docker compose -f integration/docker-compose.yml up -d --build --wait

integration-down:
	docker compose -f integration/docker-compose.yml down -v

integration-test: integration-up
	bash integration/run.sh; s=$$?; $(MAKE) integration-down; exit $$s
